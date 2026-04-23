package clickhouse

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/towertail/server/internal/config"
	"github.com/towertail/server/internal/runtime"
)

// Client wraps a ClickHouse native driver connection pool and the
// ingest batcher. Implements runtime.Component so the HTTP server can
// lazily dial on first ingest.
type Client struct {
	cfg     config.ClickHouseConfig
	log     *slog.Logger
	conn    driver.Conn
	ready   atomic.Bool
	batcher *batcher
}

func New(cfg config.ClickHouseConfig, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{cfg: cfg, log: log}
}

func (c *Client) Name() string  { return "clickhouse" }
func (c *Client) Ready() bool   { return c.ready.Load() }
func (c *Client) Conn() driver.Conn { return c.conn }

func (c *Client) Start(ctx context.Context) error {
	opts := &ch.Options{
		Addr: []string{c.cfg.Addr},
		Auth: ch.Auth{
			Database: c.cfg.Database,
			Username: c.cfg.User,
			Password: c.cfg.Password,
		},
		DialTimeout:     c.cfg.DialTimeout,
		MaxOpenConns:    c.cfg.MaxOpenConns,
		MaxIdleConns:    c.cfg.MaxIdleConns,
		ConnMaxLifetime: c.cfg.ConnMaxLifetime,
		Settings: ch.Settings{
			"async_insert": 0, // batches are already grouped in-process
		},
	}
	conn, err := ch.Open(opts)
	if err != nil {
		return fmt.Errorf("clickhouse open: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := conn.Ping(pingCtx); err != nil {
		return fmt.Errorf("clickhouse ping: %w", err)
	}
	c.conn = conn

	if err := c.ApplyMigrations(ctx); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}

	c.batcher = newBatcher(c, c.log)
	c.batcher.start()
	c.ready.Store(true)
	c.log.Info("clickhouse: ready", "addr", c.cfg.Addr, "database", c.cfg.Database)
	return nil
}

func (c *Client) Stop(ctx context.Context) error {
	if c.batcher != nil {
		c.batcher.stop()
	}
	c.ready.Store(false)
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Batcher exposes the async ingest batcher. Handlers call Enqueue on the
// returned value.
func (c *Client) Batcher() *batcher { return c.batcher }

// Ensure Client satisfies runtime.Component at compile time.
var _ runtime.Component = (*Client)(nil)
