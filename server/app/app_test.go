package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/store"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ClickHouse.Disabled = true
	cfg.Storage.Path = t.TempDir()
	cfg.HTTP.Addr = "127.0.0.1:0"
	cfg.HTTP.ShutdownTimeout = time.Second
	return *cfg
}

func quietLog() Option { return WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))) }

func TestWithRoutes(t *testing.T) {
	a, err := New(testConfig(t), quietLog(), WithRoutes(func(r Router) {
		r.Group("/v1/x", store.TokenKindUser).GET("/ping", func(c echo.Context) error {
			return c.String(http.StatusOK, "pong")
		})
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	raw, _, err := auth.NewIssuer(a.store).Issue(context.Background(), store.ZeroOrg, store.TokenKindUser, "test", nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		token string
		want  int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"user token", raw, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/x/ping", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			a.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestWithMiddleware(t *testing.T) {
	a, err := New(testConfig(t), quietLog(), WithMiddleware(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set("X-Test", "1")
			return next(c)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Header().Get("X-Test") != "1" {
		t.Fatalf("middleware did not run: %v", rec.Header())
	}
}

// fakeComponent records the calls in order.
type fakeComponent struct {
	mu      sync.Mutex
	calls   []string
	started chan struct{}
}

func (f *fakeComponent) Name() string { return "fake" }
func (f *fakeComponent) Ready() bool  { return true }

func (f *fakeComponent) Start(context.Context) error {
	f.record("start")
	close(f.started)
	return nil
}

func (f *fakeComponent) Stop(context.Context) error {
	f.record("stop")
	return nil
}

func (f *fakeComponent) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func TestWithComponent(t *testing.T) {
	c := &fakeComponent{started: make(chan struct{})}
	a, err := New(testConfig(t), quietLog(), WithComponent(c))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case <-c.started:
	case <-time.After(5 * time.Second):
		t.Fatal("component did not start")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) != 2 || c.calls[0] != "start" || c.calls[1] != "stop" {
		t.Fatalf("calls = %v, want [start stop]", c.calls)
	}
}

// memStore is a store that is not Bolt, so New opens a separate scan file.
type memStore struct{ store.ConfigStore }

func TestWithStoreNotBolt(t *testing.T) {
	bs, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bs.Close() })
	a, err := New(testConfig(t), quietLog(), WithStore(memStore{bs}))
	if err != nil {
		t.Fatal(err)
	}
	a.close()
	// The App does not close a store that the caller gave.
	if _, err := bs.ListNodes(context.Background(), store.ZeroOrg); err != nil {
		t.Fatalf("store closed by app: %v", err)
	}
}
