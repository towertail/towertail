package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/google/uuid"
	"github.com/urfave/cli/v3"

	"github.com/towertail/towertail/server/app"
	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/postgres"
	"github.com/towertail/towertail/server/internal/version"
	"github.com/towertail/towertail/server/store"
)

func main() {
	cmd := &cli.Command{
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
					{
						Name:  "issue-bulk",
						Usage: "make N nodes with one sampler token each, as CSV (node_id,token)",
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "kind", Value: "sampler", Usage: "only sampler"},
							&cli.IntFlag{Name: "count", Usage: "number of nodes", Required: true},
							&cli.StringFlag{Name: "org", Usage: "org UUID (default: the self-hosted org)"},
							&cli.StringFlag{Name: "prefix", Value: "load", Usage: "node name prefix"},
							&cli.StringFlag{Name: "out", Usage: "CSV file (default: stdout)"},
						},
						Action: runTokenIssueBulk,
					},
				},
			},
		},
	}
	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadConfig(c *cli.Command) (*config.Config, *slog.Logger, error) {
	cfg, err := config.Load(c.String("config"))
	if err != nil {
		return nil, nil, err
	}
	return cfg, cfg.Log.NewLogger(), nil
}

func runServe(ctx context.Context, c *cli.Command) error {
	cfg, log, err := loadConfig(c)
	if err != nil {
		return err
	}
	a, err := app.New(*cfg, app.WithLogger(log))
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return a.Run(ctx)
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

func runTokenIssueBulk(ctx context.Context, c *cli.Command) error {
	if c.String("kind") != string(store.TokenKindSampler) {
		return fmt.Errorf("issue-bulk: only --kind=sampler")
	}
	n := int(c.Int("count"))
	if n < 1 {
		return fmt.Errorf("issue-bulk: --count must be 1 or more")
	}
	orgID := store.ZeroOrg
	if v := c.String("org"); v != "" {
		parsed, err := uuid.Parse(v)
		if err != nil {
			return fmt.Errorf("--org: %w", err)
		}
		orgID = parsed
	}
	cfg, log, err := loadConfig(c)
	if err != nil {
		return err
	}
	st, err := store.OpenBolt(cfg.Storage.Path)
	if err != nil {
		return err
	}
	defer st.Close()

	var w io.Writer = os.Stdout
	if p := c.String("out"); p != "" {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	samplers, err := app.IssueSamplers(ctx, st, orgID, n, c.String("prefix"))
	if err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"node_id", "token"})
	for _, s := range samplers {
		_ = cw.Write([]string{s.NodeID.String(), s.Token})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return err
	}
	log.Info("sampler tokens issued", "count", len(samplers), "org_id", orgID)
	return nil
}
