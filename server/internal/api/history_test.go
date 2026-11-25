package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

type fixedLimits int

func (d fixedLimits) HistoryDays(context.Context, uuid.UUID) int { return int(d) }
func (d fixedLimits) MaxNodes(context.Context, uuid.UUID) int    { return -1 }

func rangeCtx(t *testing.T, query string) echo.Context {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/nodes/x/history?"+query, nil)
	c := e.NewContext(req, httptest.NewRecorder())
	c.SetParamNames("id")
	c.SetParamValues(uuid.NewString())
	authctx.Set(c, store.Token{OrgID: uuid.New(), Kind: store.TokenKindUser})
	return c
}

// fleetCtx is a context without :id, as the fleet route has it.
func fleetCtx(t *testing.T, query string) echo.Context {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/history?"+query, nil)
	c := e.NewContext(req, httptest.NewRecorder())
	authctx.Set(c, store.Token{OrgID: uuid.New(), Kind: store.TokenKindUser})
	return c
}

func TestParseRangeClampsToPlan(t *testing.T) {
	s := &Server{query: query.NewService(nil, nil, fixedLimits(30))}
	r, err := s.parseRange(rangeCtx(t, "from=now-2160h"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Clamped {
		t.Fatal("clamped = false, want true")
	}
	if got := time.Since(r.From); got > 30*24*time.Hour+time.Minute || got < 30*24*time.Hour-time.Minute {
		t.Fatalf("from is %v ago, want 30 days", got)
	}
}

func TestParseRangeInsidePlan(t *testing.T) {
	s := &Server{query: query.NewService(nil, nil, fixedLimits(30))}
	r, err := s.parseRange(rangeCtx(t, "from=now-24h"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if r.Clamped {
		t.Fatal("clamped = true, want false")
	}
}

func TestParseRangeNoLimits(t *testing.T) {
	for _, s := range []*Server{{query: query.NewService(nil, nil, nil)}, {query: query.NewService(nil, nil, fixedLimits(0))}} {
		r, err := s.parseRange(rangeCtx(t, "from=now-2160h"), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if r.Clamped || time.Since(r.From) < 89*24*time.Hour {
			t.Fatalf("range = %+v, want no clamp", r)
		}
	}
}

// A range that ends before the oldest allowed time becomes empty.
func TestParseRangeToBeforeLimit(t *testing.T) {
	s := &Server{query: query.NewService(nil, nil, fixedLimits(30))}
	r, err := s.parseRange(rangeCtx(t, "from=now-2160h&to=now-1440h"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Clamped || !r.To.Equal(r.From) {
		t.Fatalf("range = %+v, want an empty clamped range", r)
	}
}

func TestParseRangeWithoutID(t *testing.T) {
	s := &Server{query: query.NewService(nil, nil, nil)}
	r, err := s.parseRange(fleetCtx(t, "from=now-2h"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.To.Sub(r.From); got < 119*time.Minute || got > 121*time.Minute {
		t.Fatalf("range = %v, want 2h", got)
	}
}

func TestParseIDListLimits(t *testing.T) {
	ids := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = uuid.NewString()
		}
		return strings.Join(parts, ",")
	}
	if _, err := parseIDList(ids(201), 200); err == nil {
		t.Error("201 ids: want an error")
	}
	if got, err := parseIDList(ids(200), 200); err != nil || len(got) != 200 {
		t.Errorf("200 ids: %d, %v", len(got), err)
	}
	id := uuid.NewString()
	if got, err := parseIDList(id+", "+id+","+id, 200); err != nil || len(got) != 1 {
		t.Errorf("duplicates: %v, %v, want one id", got, err)
	}
	if _, err := parseIDList(id+",nope", 200); err == nil {
		t.Error("bad uuid: want an error")
	}
	if _, err := parseIDList("", 200); err == nil {
		t.Error("empty: want an error")
	}
}

func TestParseMetricListLimits(t *testing.T) {
	if _, err := parseMetricList("a,b,c,d,e,f,g,h,i", 8); err == nil {
		t.Error("9 names: want an error")
	}
	names := "a,b,c,d,e,f,g,h,i,j,k,l,m,n,o,p"
	if got, err := parseMetricList(names, maxHistoryMetrics); err != nil || len(got) != 16 {
		t.Errorf("16 names: %v, %v", got, err)
	}
	if _, err := parseMetricList(names+",q", maxHistoryMetrics); err == nil {
		t.Error("17 names: want an error")
	}
	if got, err := parseMetricList("cpu, mem,cpu,", 8); err != nil || len(got) != 2 || got[0] != "cpu" || got[1] != "mem" {
		t.Errorf("list = %v, %v, want [cpu mem]", got, err)
	}
	for _, in := range []string{"", " , "} {
		if _, err := parseMetricList(in, 8); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
}

func TestFleetHistoryRejectsLongRange(t *testing.T) {
	s := &Server{query: query.NewService(nil, nil, nil)}
	q := "nodes=" + uuid.NewString() + "&metrics=cpu&from=now-48h"
	err := s.handleFleetHistory(fleetCtx(t, q))
	he, ok := err.(*echo.HTTPError)
	if !ok || he.Code != http.StatusBadRequest {
		t.Fatalf("err = %v, want 400", err)
	}
}

func TestFleetHistoryBadParams(t *testing.T) {
	s := &Server{query: query.NewService(nil, nil, nil)}
	id := uuid.NewString()
	for _, q := range []string{"metrics=cpu", "nodes=" + id, "nodes=nope&metrics=cpu", "nodes=" + id + "&metrics=a,b,c,d,e,f,g,h,i"} {
		err := s.handleFleetHistory(fleetCtx(t, q))
		if he, ok := err.(*echo.HTTPError); !ok || he.Code != http.StatusBadRequest {
			t.Errorf("%q: err = %v, want 400", q, err)
		}
	}
}

func TestFleetRangeClampsToPlan(t *testing.T) {
	s := &Server{query: query.NewService(nil, nil, fixedLimits(1))}
	r, err := s.parseFleetRange(fleetCtx(t, "from=now-30h&to=now-8h"))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Clamped {
		t.Fatal("clamped = false, want true")
	}
	if got := time.Since(r.From); got > 24*time.Hour+time.Minute || got < 24*time.Hour-time.Minute {
		t.Fatalf("from is %v ago, want 24h", got)
	}
}

func TestFleetRangeDefault(t *testing.T) {
	s := &Server{query: query.NewService(nil, nil, nil)}
	r, err := s.parseFleetRange(fleetCtx(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.To.Sub(r.From); got != 30*time.Minute {
		t.Fatalf("range = %v, want 30m", got)
	}
}

func TestHistoryMultiResponseShape(t *testing.T) {
	id := uuid.New()
	out := wire.NodeHistoryMultiResponse{
		NodeHistorySeries: wire.NodeHistorySeries{NodeID: id, Series: map[string][]wire.NodeHistoryPoint{
			"cpu": {{TS: time.Unix(0, 0).UTC(), Value: 1, Max: 2}},
		}},
		Resolution: "raw",
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"node_id", "series", "from", "to", "resolution"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %s", k, b)
		}
	}
	if _, ok := m["clamped"]; ok {
		t.Errorf("clamped must be omitted when false: %s", b)
	}

	old, _ := json.Marshal(wire.NodeHistoryResponse{NodeID: id, Metric: "cpu", Resolution: "raw", Points: []wire.NodeHistoryPoint{}})
	if want := `{"node_id":"` + id.String() + `","metric":"cpu","resolution":"raw","points":[]}`; string(old) != want {
		t.Errorf("old shape = %s, want %s", old, want)
	}
}
