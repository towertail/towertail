package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/runtime"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

func TestHandleProcessesAt(t *testing.T) {
	e := newKeyEnv(t, config.HTTPConfig{})
	user, _ := e.login(t, store.RoleViewer)
	base := "/v1/nodes/" + uuid.NewString() + "/processes/at"
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"?ts=yesterday", http.StatusBadRequest},
		{"?limit=0", http.StatusBadRequest},
		{"?limit=51", http.StatusBadRequest},
		{"?limit=x", http.StatusBadRequest},
		{"?by=disk", http.StatusBadRequest},
		// Valid values reach requireCH. The server has no ClickHouse.
		{"", http.StatusServiceUnavailable},
		{"?ts=2026-01-02T03:04:05Z&by=io&limit=50", http.StatusServiceUnavailable},
		{"?ts=now-1h", http.StatusServiceUnavailable},
	} {
		if rec := e.do(http.MethodGet, base+tc.query, user, nil); rec.Code != tc.want {
			t.Errorf("GET %s: %d %s, want %d", tc.query, rec.Code, rec.Body, tc.want)
		}
	}
	if rec := e.do(http.MethodGet, "/v1/nodes/x/processes/at", user, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: %d", rec.Code)
	}
}

type readyComponent struct{}

func (readyComponent) Name() string                { return "clickhouse" }
func (readyComponent) Start(context.Context) error { return nil }
func (readyComponent) Stop(context.Context) error  { return nil }
func (readyComponent) Ready() bool                 { return true }

func TestHandleProcessesAtClamped(t *testing.T) {
	rt := runtime.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	rt.Register(readyComponent{})
	// The empty client has no connection: a query would panic.
	s := &Server{ch: &clickhouse.Client{}, runtime: rt, query: query.NewService(nil, nil, fixedLimits(7), nil)}
	req := httptest.NewRequest(http.MethodGet, "/v1/nodes/x/processes/at?ts=now-720h", nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(uuid.NewString())
	authctx.Set(c, store.Token{OrgID: uuid.New(), Kind: store.TokenKindUser})
	if err := s.handleProcessesAt(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || !jsonHas(rec.Body.Bytes(), `"clamped":true`) || !jsonHas(rec.Body.Bytes(), `"processes":[]`) {
		t.Fatalf("response: %d %s", rec.Code, rec.Body)
	}
}

func jsonHas(b []byte, s string) bool { return strings.Contains(string(b), s) }

func TestHandleProcessesSeriesParam(t *testing.T) {
	e := newKeyEnv(t, config.HTTPConfig{})
	user, _ := e.login(t, store.RoleViewer)
	base := "/v1/nodes/" + uuid.NewString() + "/processes"
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"?series=121", http.StatusBadRequest},
		{"?series=-1", http.StatusBadRequest},
		{"?series=abc", http.StatusBadRequest},
		// Valid values reach requireCH. The server has no ClickHouse.
		{"?series=0", http.StatusServiceUnavailable},
		{"?series=120", http.StatusServiceUnavailable},
	} {
		if rec := e.do(http.MethodGet, base+tc.query, user, nil); rec.Code != tc.want {
			t.Errorf("GET %s: %d %s, want %d", tc.query, rec.Code, rec.Body, tc.want)
		}
	}
}

func TestDenseSeries(t *testing.T) {
	from := time.Unix(6000, 0).UTC()
	step := time.Minute
	pts := []clickhouse.ProcPoint{
		{TS: from.Add(step), CPUPct: 10, RSS: 5, IOBps: 2},
		{TS: from.Add(3 * step), CPUPct: 30, RSS: 7},
		{TS: from.Add(-step), CPUPct: 99},
		{TS: from.Add(9 * step), CPUPct: 99},
	}
	got := denseSeries(pts, from, step, 5)
	want := &wire.ProcSeries{
		CPU: []float64{0, 10, 0, 30, 0},
		RSS: []uint64{0, 5, 0, 7, 0},
		IO:  []float64{0, 2, 0, 0, 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("series = %+v, want %+v", got, want)
	}
	if got := denseSeries(nil, from, step, 3); len(got.CPU) != 3 || len(got.RSS) != 3 {
		t.Fatalf("empty series = %+v", got)
	}
}

func TestAlignDown(t *testing.T) {
	at := time.Unix(1_000_000_123, 456)
	if got := alignDown(at, 7*time.Minute); got.Unix()%420 != 0 || at.Sub(got) >= 7*time.Minute || got.After(at) {
		t.Fatalf("alignDown = %v", got)
	}
}

func TestHandleProcess(t *testing.T) {
	e := newKeyEnv(t, config.HTTPConfig{})
	user, _ := e.login(t, store.RoleViewer)
	node := "/v1/nodes/" + uuid.NewString() + "/processes/"
	key := wire.NewProcessKey("nginx", "/usr/sbin/nginx").String()
	for _, tc := range []struct {
		path string
		want int
	}{
		{"bad", http.StatusBadRequest},
		{"zz.bmFtZQ", http.StatusBadRequest},
		{"0123456789abcdef.", http.StatusBadRequest},
		// A valid key reaches requireCH. The server has no ClickHouse.
		{key, http.StatusServiceUnavailable},
		{key + "?from=now-3h&to=now-2h&step=5m", http.StatusServiceUnavailable},
		// The static route "at" still goes to the snapshot handler.
		{"at?limit=0", http.StatusBadRequest},
		{"at", http.StatusServiceUnavailable},
	} {
		if rec := e.do(http.MethodGet, node+tc.path, user, nil); rec.Code != tc.want {
			t.Errorf("GET %s: %d %s, want %d", tc.path, rec.Code, rec.Body, tc.want)
		}
	}
	if rec := e.do(http.MethodGet, "/v1/nodes/x/processes/"+key, user, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: %d", rec.Code)
	}
}
