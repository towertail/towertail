// Package app builds and runs the Towertail server. Other modules (the
// managed cloud) use it to add their own store, routes, notifiers and
// components.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/alerter"
	"github.com/towertail/towertail/server/internal/api"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/control"
	"github.com/towertail/towertail/server/internal/hub"
	"github.com/towertail/towertail/server/internal/postgres"
	"github.com/towertail/towertail/server/internal/scan"
	"github.com/towertail/towertail/server/runtime"
	"github.com/towertail/towertail/server/store"
)

// scanSweepInterval is the time between two checks for disk scans that
// expired or have no progress.
const scanSweepInterval = 30 * time.Second

// reachabilitySweepInterval is the time between two checks for nodes
// with no recent sample.
const reachabilitySweepInterval = 10 * time.Second

// App is one server: config, runtime, store, ClickHouse, hub, alerter
// and API.
type App struct {
	cfg     *config.Config
	log     *slog.Logger
	rt      *runtime.Runtime
	store   store.ConfigStore
	ch      *clickhouse.Client
	hub     *hub.Hub
	alerter *alerter.Engine
	scans   *scan.Manager
	api     *api.Server
	// eager are the components that Run starts.
	eager []string
	// closers run at the end of Run, for what New opened.
	closers []func() error
}

// New builds the App. It opens the Bolt store unless WithStore is given.
func New(cfg config.Config, opts ...Option) (a *App, err error) {
	b := &builder{}
	for _, o := range opts {
		o(b)
	}
	a = &App{cfg: &cfg, log: b.log}
	if a.log == nil {
		a.log = cfg.Log.NewLogger()
	}
	defer func() {
		if err != nil {
			a.close()
		}
	}()
	ctx := context.Background()

	a.store = b.store
	if a.store == nil {
		bs, err := store.OpenBolt(cfg.Storage.Path)
		if err != nil {
			return nil, fmt.Errorf("open bolt: %w", err)
		}
		a.store = bs
		a.closers = append(a.closers, bs.Close)
	}

	issuer := auth.NewIssuer(a.store)
	if tok := cfg.Auth.BootstrapToken; tok != "" {
		if err := a.installBootstrapToken(ctx, issuer, tok); err != nil {
			a.log.Warn("bootstrap token install failed", "err", err)
		}
	}

	a.rt = runtime.New(a.log)
	if !cfg.ClickHouse.Disabled {
		a.ch = clickhouse.New(cfg.ClickHouse, a.log)
		a.rt.Register(a.ch)
		a.eager = append(a.eager, a.ch.Name())
	} else {
		a.log.Warn("clickhouse disabled via config — ingest not persisted, history unavailable")
	}
	if cfg.Cloud.Managed {
		pg := postgres.New(cfg.Postgres, a.log)
		a.rt.Register(pg)
		a.eager = append(a.eager, pg.Name())
	}
	for _, c := range b.components {
		a.rt.Register(c)
		a.eager = append(a.eager, c.Name())
	}

	a.hub = hub.New(a.log)
	a.alerter = alerter.New(a.log, nil, a.hub, chEvents{ch: a.ch}, a.store, b.notifiers...)
	ctrl := control.New(a.store)
	scanDB, err := a.scanDB()
	if err != nil {
		return nil, err
	}
	scanStore, err := scan.NewStore(scanDB)
	if err != nil {
		return nil, fmt.Errorf("open scan store: %w", err)
	}
	a.scans = scan.NewManager(scanStore, ctrl, a.hub, a.log, nil)

	routes := make([]func(*api.Server), 0, len(b.routes))
	for _, f := range b.routes {
		routes = append(routes, func(s *api.Server) { f(s) })
	}
	a.api = api.New(api.Deps{
		Config:     a.cfg,
		Log:        a.log,
		Runtime:    a.rt,
		Store:      a.store,
		CH:         a.ch,
		Issuer:     issuer,
		Hub:        a.hub,
		Alerter:    a.alerter,
		Control:    ctrl,
		Scans:      a.scans,
		Middleware: b.middleware,
		Routes:     routes,
	})
	return a, nil
}

// Handler is the HTTP handler of the API, for tests.
func (a *App) Handler() http.Handler { return a.api.Handler() }

// Run serves HTTP until ctx is done. Then it stops the components and
// closes what New opened.
func (a *App) Run(ctx context.Context) error {
	defer a.close()
	// Start the components on boot, so ClickHouse migrations apply and
	// /readyz flips to 200 without a wait for the first ingest.
	for _, name := range a.eager {
		go func() {
			if err := a.rt.Require(context.Background(), name); err != nil {
				a.log.Error("component eager start failed", "name", name, "err", err)
			}
		}()
	}
	go a.scans.Run(ctx, scanSweepInterval)
	go a.alerter.Run(ctx, reachabilitySweepInterval)

	err := a.api.Start(ctx)
	if stopErr := a.rt.StopAll(context.Background()); stopErr != nil {
		a.log.Warn("stop components", "err", stopErr)
	}
	return err
}

func (a *App) close() {
	for _, c := range a.closers {
		_ = c()
	}
	a.closers = nil
}

// scanDB is the Bolt file for disk scans: the file of the Bolt store, or
// a separate file when the store is not Bolt.
func (a *App) scanDB() (*bolt.DB, error) {
	if bs, ok := a.store.(interface{ DB() *bolt.DB }); ok {
		return bs.DB(), nil
	}
	db, err := store.OpenBoltFile(a.cfg.Storage.Path, "scans.db")
	if err != nil {
		return nil, fmt.Errorf("open scan db: %w", err)
	}
	a.closers = append(a.closers, db.Close)
	return db, nil
}

// installBootstrapToken stores the operator token from the config as an
// admin token, once.
func (a *App) installBootstrapToken(ctx context.Context, issuer *auth.TokenIssuer, raw string) error {
	if _, err := issuer.Verify(ctx, raw); err == nil {
		return nil
	}
	if raw == "" {
		return errors.New("empty bootstrap token")
	}
	err := a.store.PutToken(ctx, store.Token{
		ID:    uuid.New(),
		OrgID: store.ZeroOrg,
		Kind:  store.TokenKindAdmin,
		Label: "bootstrap",
		Hash:  auth.HashForSeed(raw),
	})
	if err == nil {
		a.log.Info("bootstrap token installed")
	}
	return err
}

var errCHNotReady = errors.New("clickhouse not ready")

// chEvents gives the alerter the events of ClickHouse. It drops events
// when ClickHouse is off or not ready.
type chEvents struct{ ch *clickhouse.Client }

func (s chEvents) WriteEvent(ctx context.Context, e clickhouse.EventRow) error {
	if s.ch == nil || !s.ch.Ready() {
		return nil
	}
	return s.ch.WriteEvent(ctx, e)
}

func (s chEvents) LastTints(ctx context.Context, since time.Time) ([]clickhouse.TintRow, error) {
	if s.ch == nil {
		return nil, nil
	}
	if !s.ch.Ready() {
		return nil, errCHNotReady
	}
	return s.ch.LastTints(ctx, since)
}

func (s chEvents) LastSeen(ctx context.Context, since time.Time) ([]clickhouse.NodeSeen, error) {
	if s.ch == nil {
		return nil, nil
	}
	if !s.ch.Ready() {
		return nil, errCHNotReady
	}
	return s.ch.LastSeen(ctx, since)
}
