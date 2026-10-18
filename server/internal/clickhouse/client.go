package clickhouse

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"regexp"
	"sync/atomic"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/runtime"
)

// Client wraps a ClickHouse native driver connection pool and the
// ingest batcher. Implements runtime.Component so the HTTP server can
// lazily dial on first ingest.
type Client struct {
	cfg     config.ClickHouseConfig
	log     *slog.Logger
	db      string
	extra   []migrationSet
	conn    driver.Conn
	ready   atomic.Bool
	batcher *batcher
}

// dbName limits the database name, because SQL text holds it.
var dbName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func New(cfg config.ClickHouseConfig, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	db := cfg.Database
	if db == "" {
		db = "towertail"
	}
	return &Client{cfg: cfg, log: log, db: db}
}

// table gives the name of a table in the database of the client.
func (c *Client) table(name string) string { return c.db + "." + name }

func (c *Client) options() *ch.Options {
	addrs := c.cfg.Addrs
	if len(addrs) == 0 {
		addrs = []string{c.cfg.Addr}
	}
	opts := &ch.Options{
		Addr: addrs,
		// No default database: the SQL names the database of each table,
		// so the migrations can create it.
		Auth: ch.Auth{
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
	if c.cfg.Secure {
		opts.TLS = &tls.Config{}
	}
	return opts
}

func (c *Client) Name() string      { return "clickhouse" }
func (c *Client) Ready() bool       { return c.ready.Load() }
func (c *Client) Conn() driver.Conn { return c.conn }

func (c *Client) Start(ctx context.Context) error {
	if !dbName.MatchString(c.db) {
		return fmt.Errorf("clickhouse: bad database name %q", c.db)
	}
	opts := c.options()
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

	sp, err := newSpill(c.cfg.SpillDir, c.cfg.SpillMaxBytes)
	if err != nil {
		return err
	}
	c.batcher = newBatcher(c.cfg, c, sp, c.log)
	c.batcher.start()
	c.ready.Store(true)
	c.log.Info("clickhouse: ready", "addr", opts.Addr, "database", c.db)
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
