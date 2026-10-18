package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
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

// chanNotifier sends each alert on a channel.
type chanNotifier chan notify.Alert

func (n chanNotifier) Name() string { return "chan" }

func (n chanNotifier) Notify(_ context.Context, a notify.Alert) error {
	n <- a
	return nil
}

func TestWithNotifier(t *testing.T) {
	n := make(chanNotifier, 4)
	a, err := New(testConfig(t), quietLog(), WithNotifier(n))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	ctx := context.Background()
	node := store.Node{ID: uuid.New(), DisplayName: "web-1", Kind: "ssh", Enabled: true, NotifyOnWarn: true, NotifyOnCritical: true}
	if err := a.store.PutNode(ctx, store.ZeroOrg, node); err != nil {
		t.Fatal(err)
	}
	raw, _, err := auth.NewIssuer(a.store).Issue(ctx, store.ZeroOrg, store.TokenKindSampler, "test", &node.ID)
	if err != nil {
		t.Fatal(err)
	}
	ingest := func(cpuPct float64) {
		t.Helper()
		line := fmt.Sprintf(`{"v":1,"ts":%q,"host":{"name":"web-1","os":"linux","arch":"amd64"},"cpu":{"pct":%g,"cores":4},"mem":{"used":1,"total":100},"swap":{"used":0,"total":0},"errors":[]}`,
			time.Now().UTC().Format(time.RFC3339Nano), cpuPct)
		req := httptest.NewRequest(http.MethodPost, "/v1/ingest/samples", strings.NewReader(line+"\n"))
		req.Header.Set("Authorization", "Bearer "+raw)
		req.Header.Set("Content-Type", "application/x-ndjson")
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		if rec.Code >= 300 {
			t.Fatalf("ingest: %d %s", rec.Code, rec.Body)
		}
	}
	ingest(10)
	ingest(95)
	select {
	case got := <-n:
		if got.NodeID != node.ID || got.Metric != "cpu" || got.To != wire.TintCritical {
			t.Fatalf("alert = %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no alert")
	}
}
