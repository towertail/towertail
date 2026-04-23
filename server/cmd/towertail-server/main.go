package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/google/uuid"
	"github.com/urfave/cli/v3"

	"github.com/towertail/server/internal/alerter"
	"github.com/towertail/server/internal/api"
	"github.com/towertail/server/internal/auth"
	"github.com/towertail/server/internal/clickhouse"
	"github.com/towertail/server/internal/config"
	"github.com/towertail/server/internal/control"
	"github.com/towertail/server/internal/hub"
	"github.com/towertail/server/internal/postgres"
	pkgruntime "github.com/towertail/server/internal/runtime"
	"github.com/towertail/server/internal/store"
	"github.com/towertail/server/internal/version"
)

func main() {
	app := &cli.Command{
		Name:    "towertail-server",
		Usage:   "Towertail control-plane server",
		Version: version.Version + " " + version.SHA,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Usage: "path to YAML config", Sources: cli.EnvVars("TT_CONFIG_FILE")},
		},
		Commands: []*cli.Command{
			{
				Name:   "serve",
				Usage:  "start the HTTP server",
				Action: runServe,
			},
			{
				Name:  "migrate",
				Usage: "run database migrations",
				Commands: []*cli.Command{
					{Name: "clickhouse", Action: runMigrateClickHouse},
					{Name: "postgres", Action: runMigratePostgres},
				},
			},
			{
				Name:  "token",
				Usage: "manage bearer tokens",
				Commands: []*cli.Command{
					{
						Name:  "issue",
						Usage: "issue a new token",
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "kind", Usage: "user|sampler|admin", Required: true},
							&cli.StringFlag{Name: "label", Usage: "human label"},
							&cli.StringFlag{Name: "node-id", Usage: "node UUID for sampler tokens"},
						},
						Action: runTokenIssue,
					},
				},
			},
		},
	}
	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadConfig(c *cli.Command) (*config.Config, *slog.Logger, error) {
	cfg, err := config.Load(c.String("config"))
	if err != nil {
		return nil, nil, err
	}
	log := newLogger(cfg.Log)
	return cfg, log, nil
}

func newLogger(cfg config.LogConfig) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.Format == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

func runServe(ctx context.Context, c *cli.Command) error {
	cfg, log, err := loadConfig(c)
	if err != nil {
		return err
	}

	rt := pkgruntime.New(log)
	st, err := store.OpenBolt(cfg.Storage.Path)
	if err != nil {
		return fmt.Errorf("open bolt: %w", err)
	}

	// Ensure there's at least one token usable by the user group. A
	// bootstrap token from config gets installed once (idempotent by
	// hash-collision rarity; reinstall on every boot is fine).
	issuer := auth.NewIssuer(st)
	if tok := cfg.Auth.BootstrapToken; tok != "" {
		_, existing := store.Token{}, false
		if _, err := issuer.Verify(ctx, tok); err == nil {
			existing = true
		}
		if !existing {
			if err := installBootstrapToken(ctx, st, tok); err != nil {
				log.Warn("bootstrap token install failed", "err", err)
			} else {
				log.Info("bootstrap token installed")
			}
		}
	}

	var ch *clickhouse.Client
	if !cfg.ClickHouse.Disabled {
		ch = clickhouse.New(cfg.ClickHouse, log)
	} else {
		log.Warn("clickhouse disabled via config — ingest not persisted, history unavailable")
	}
	pg := postgres.New(cfg.Postgres, log)
	h := hub.New(log)

	settings, err := st.GetSettings(ctx, store.ZeroOrg)
	if err != nil {
		return err
	}
	alerterEngine := alerter.New(log, nil, h, chStoreShim{ch: ch}, settings)
	ctrl := control.New(st)

	if ch != nil {
		rt.Register(ch)
	}
	if cfg.Cloud.Managed {
		rt.Register(pg)
	}

	server := api.New(api.Deps{
		Config:  cfg,
		Log:     log,
		Runtime: rt,
		Store:   st,
		CH:      ch,
		Issuer:  issuer,
		Hub:     h,
		Alerter: alerterEngine,
		Control: ctrl,
	})

	// Trigger lazy start of ClickHouse on boot so migrations apply and
	// /readyz flips to 200 without waiting for the first ingest. Skipped
	// when CH is disabled — /readyz then reports ready unconditionally
	// (see handleReadyz).
	if ch != nil {
		go func() {
			if err := rt.Require(context.Background(), "clickhouse"); err != nil {
				log.Error("clickhouse eager start failed", "err", err)
			}
		}()
	}
	if cfg.Cloud.Managed {
		go func() {
			if err := rt.Require(context.Background(), "postgres"); err != nil {
				log.Error("postgres eager start failed", "err", err)
			}
		}()
	}

	sigCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	errCh := make(chan error, 1)
	go func() {
		defer wg.Done()
		errCh <- server.Start(sigCtx)
	}()

	select {
	case err := <-errCh:
		_ = rt.StopAll(ctx)
		_ = st.Close()
		return err
	case <-sigCtx.Done():
		log.Info("shutdown: signal received")
		err := server.Start(sigCtx) // returns immediately because ctx cancelled
		wg.Wait()
		_ = rt.StopAll(ctx)
		_ = st.Close()
		return err
	}
}

func runMigrateClickHouse(ctx context.Context, c *cli.Command) error {
	cfg, log, err := loadConfig(c)
	if err != nil {
		return err
	}
	ch := clickhouse.New(cfg.ClickHouse, log)
	if err := ch.Start(ctx); err != nil {
		return err
	}
	defer ch.Stop(ctx)
	log.Info("clickhouse migrations complete")
	return nil
}

func runMigratePostgres(ctx context.Context, c *cli.Command) error {
	cfg, log, err := loadConfig(c)
	if err != nil {
		return err
	}
	if err := postgres.ApplyMigrations(ctx, cfg.Postgres.DSN); err != nil {
		return err
	}
	log.Info("postgres migrations complete")
	return nil
}

func runTokenIssue(ctx context.Context, c *cli.Command) error {
	cfg, log, err := loadConfig(c)
	if err != nil {
		return err
	}
	st, err := store.OpenBolt(cfg.Storage.Path)
	if err != nil {
		return err
	}
	defer st.Close()
	issuer := auth.NewIssuer(st)

	kind := store.TokenKind(c.String("kind"))
	switch kind {
	case store.TokenKindUser, store.TokenKindSampler, store.TokenKindAdmin:
	default:
		return fmt.Errorf("invalid kind %q", kind)
	}
	var nodeID *uuid.UUID
	if v := c.String("node-id"); v != "" {
		parsed, err := uuid.Parse(v)
		if err != nil {
			return err
		}
		nodeID = &parsed
	}
	raw, t, err := issuer.Issue(ctx, store.ZeroOrg, kind, c.String("label"), nodeID)
	if err != nil {
		return err
	}
	log.Info("token issued", "id", t.ID, "kind", t.Kind)
	fmt.Println(raw)
	return nil
}

// installBootstrapToken writes the raw secret at boot. Only admin kind;
// the operator copies the token out-of-band and treats any further
// boots as no-ops on a hash match.
func installBootstrapToken(ctx context.Context, st store.ConfigStore, raw string) error {
	issuer := auth.NewIssuer(st)
	if _, err := issuer.Verify(ctx, raw); err == nil {
		return nil
	}
	// Install via the issuer's hashing path, but with a fixed raw value
	// — we can't do that directly because Issue generates a random raw.
	// Use a small helper: hash the supplied token ourselves and store it
	// under an admin kind entry.
	return installSeededToken(ctx, st, raw)
}

// installSeededToken is a tiny helper for the bootstrap path: takes an
// operator-provided raw token and stores its sha256 as an admin token.
func installSeededToken(ctx context.Context, st store.ConfigStore, raw string) error {
	if raw == "" {
		return errors.New("empty bootstrap token")
	}
	tok := store.Token{
		ID:    uuid.New(),
		OrgID: store.ZeroOrg,
		Kind:  store.TokenKindAdmin,
		Label: "bootstrap",
		Hash:  auth.HashForSeed(raw),
	}
	return st.PutToken(ctx, tok)
}

// chStoreShim adapts the ClickHouse client to the alerter's Store
// interface (it only needs WriteEvent).
type chStoreShim struct{ ch *clickhouse.Client }

func (s chStoreShim) WriteEvent(ctx context.Context, e clickhouse.EventRow) error {
	if s.ch == nil || !s.ch.Ready() {
		return nil
	}
	return s.ch.WriteEvent(ctx, e)
}
