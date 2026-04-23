package postgres

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/towertail/server/internal/config"
	"github.com/towertail/server/internal/runtime"
)

// Client wraps the pgx pool as a lazy runtime.Component. In self-hosted
// mode the component is registered but never started (nothing requires
// it); flipping cloud_managed=true activates it.
type Client struct {
	cfg   config.PostgresConfig
	log   *slog.Logger
	pool  *pgxpool.Pool
	ready atomic.Bool
}

func New(cfg config.PostgresConfig, log *slog.Logger) *Client {
	return &Client{cfg: cfg, log: log}
}

func (c *Client) Name() string { return "postgres" }
func (c *Client) Ready() bool  { return c.ready.Load() }
func (c *Client) Pool() *pgxpool.Pool { return c.pool }

func (c *Client) Start(ctx context.Context) error {
	if c.cfg.DSN == "" {
		c.log.Info("postgres: DSN empty, component idle")
		return nil
	}
	cfg, err := pgxpool.ParseConfig(c.cfg.DSN)
	if err != nil {
		return err
	}
	if c.cfg.MaxConns > 0 {
		cfg.MaxConns = int32(c.cfg.MaxConns)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return err
	}
	c.pool = pool
	c.ready.Store(true)
	c.log.Info("postgres: ready")
	return nil
}

func (c *Client) Stop(context.Context) error {
	c.ready.Store(false)
	if c.pool != nil {
		c.pool.Close()
	}
	return nil
}

var _ runtime.Component = (*Client)(nil)
