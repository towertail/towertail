package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
)

func points(values ...float64) []clickhouse.HistoryPoint {
	var out []clickhouse.HistoryPoint
	for i, v := range values {
		out = append(out, clickhouse.HistoryPoint{TS: now.Add(-time.Duration(len(values)-i) * time.Minute), Value: v, Max: v})
	}
	return out
}

func TestHostMetricsAllMetrics(t *testing.T) {
	f := newFake()
	f.points = points(1, 2)
	cs := connect(t, f, tokenA())
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	for _, tl := range list.Tools {
		if tl.Name == "host_metrics" {
			b, _ := json.Marshal(tl.InputSchema)
			schema = string(b)
		}
	}
	for _, m := range clickhouse.MetricNames() {
		if !strings.Contains(schema, m) {
			t.Errorf("the input schema does not name %s", m)
		}
		out := decode[HostMetricsOut](t, call(t, cs, "host_metrics", map[string]any{"host": "web-1", "metrics": []string{m}}))
		if sr := out.Hosts[0].Metrics[0]; sr.Metric != m || sr.Unit == "" || sr.Count != 2 {
			t.Errorf("%s: series = %+v", m, sr)
		}
	}
	res := call(t, cs, "host_metrics", map[string]any{"host": "web-1", "metrics": []string{"bogus"}})
	if !res.IsError || !strings.Contains(text(res), "psi_mem_full") {
		t.Errorf("bogus: %q, want a tool error with the valid names", text(res))
	}
}

func TestHostMetricsManyHosts(t *testing.T) {
	f := newFake()
	f.points = points(5)
	cs := connect(t, f, tokenA())

	out := decode[HostMetricsOut](t, call(t, cs, "host_metrics", map[string]any{"hosts": []string{"web-1", "db-1", "web-1"}}))
	if len(out.Hosts) != 2 || out.Hosts[0].Host != "db-1" || out.Hosts[1].Host != "web-1" || len(f.gotHist.NodeIDs) != 2 {
		t.Fatalf("hosts = %+v", out.Hosts)
	}
	out = decode[HostMetricsOut](t, call(t, cs, "host_metrics", map[string]any{"tag": "web"}))
	if len(out.Hosts) != 1 || out.Hosts[0].Host != "web-1" {
		t.Fatalf("tag hosts = %+v", out.Hosts)
	}

	many := make([]string, 51)
	for i := range many {
		many[i] = "web-1"
	}
	res := call(t, cs, "host_metrics", map[string]any{"hosts": many})
	if !res.IsError || !strings.Contains(text(res), "too many entries") {
		t.Errorf("51 hosts: IsError = %v, text = %q", res.IsError, text(res))
	}
	res = call(t, cs, "host_metrics", map[string]any{"hosts": []string{"web-1", "db-1"}, "metrics": clickhouse.MetricNames()[:9]})
	if !res.IsError || !strings.Contains(text(res), "at most 8") {
		t.Errorf("9 metrics for 2 hosts: IsError = %v, text = %q", res.IsError, text(res))
	}
	one := decode[HostMetricsOut](t, call(t, cs, "host_metrics", map[string]any{"host": "web-1", "metrics": clickhouse.MetricNames()[:16]}))
	if len(one.Hosts[0].Metrics) != 16 {
		t.Errorf("16 metrics for one host: got %d", len(one.Hosts[0].Metrics))
	}
}

func TestHostMetricsPointBudget(t *testing.T) {
	f := newFake()
	for i := 0; i < 50; i++ {
		f.nodes = append(f.nodes, store.Node{ID: uuid.New(), DisplayName: "n" + string(rune('A'+i%26)) + string(rune('a'+i/26))})
	}
	f.points = points(make([]float64, 200)...)
	var refs []string
	for _, n := range f.nodes[2:] {
		refs = append(refs, n.DisplayName)
	}
	cs := connect(t, f, tokenA())
	out := decode[HostMetricsOut](t, call(t, cs, "host_metrics", map[string]any{
		"hosts": refs, "metrics": clickhouse.MetricNames()[:8], "range": "24h",
	}))
	if len(out.Hosts) != 50 {
		t.Fatalf("hosts = %d", len(out.Hosts))
	}
	for _, h := range out.Hosts {
		for _, sr := range h.Metrics {
			if len(sr.Points) == 0 || len(sr.Points) > 20 || sr.Count != 200 {
				t.Fatalf("%s/%s: %d points, count %d; want 1 to 20 points, count 200", h.Host, sr.Metric, len(sr.Points), sr.Count)
			}
		}
	}
}

func TestHostMetricsCompare(t *testing.T) {
	f := newFake()
	f.points = points(10, 20)
	f.prev = points(10, 10)
	f.hostPoints = map[uuid.UUID][]clickhouse.HistoryPoint{f.nodes[1].ID: points(100, 100)}
	cs := connect(t, f, tokenA())

	out := decode[HostMetricsOut](t, call(t, cs, "host_metrics", map[string]any{"hosts": []string{"web-1", "db-1"}, "compare": true, "range": "1h"}))
	if !f.gotHist.Previous || out.Previous == nil || out.Previous.To != "2026-11-20T11:00:00Z" || out.Previous.From != "2026-11-20T10:00:00Z" {
		t.Fatalf("previous window = %+v", out.Previous)
	}
	// db-1 changes by 90, web-1 by 5, so db-1 comes first.
	if out.Hosts[0].Host != "db-1" || out.Hosts[1].Host != "web-1" {
		t.Fatalf("order = %s, %s", out.Hosts[0].Host, out.Hosts[1].Host)
	}
	sr := out.Hosts[1].Metrics[0]
	if sr.Previous == nil || sr.Previous.Avg != 10 || sr.Previous.Count != 2 || sr.ChangeAvg == nil || *sr.ChangeAvg != 5 {
		t.Fatalf("web-1 series = %+v", sr)
	}
	if got := *out.Hosts[0].Metrics[0].ChangeAvg; got != 90 {
		t.Errorf("db-1 change = %v, want 90", got)
	}

	// Metrics of one host: the biggest change first.
	f.points, f.hostPoints = points(10, 10), nil
	one := decode[HostMetricsOut](t, call(t, cs, "host_metrics", map[string]any{"host": "web-1", "metrics": []string{"cpu", "mem"}, "compare": true}))
	if !slices.IsSortedFunc(one.Hosts[0].Metrics, func(a, b Series) int { return strings.Compare(a.Metric, b.Metric) }) {
		t.Errorf("equal changes must keep the name order: %+v", one.Hosts[0].Metrics)
	}

	// No data before the range: no previous values and no change.
	f.prev = nil
	none := decode[HostMetricsOut](t, call(t, cs, "host_metrics", map[string]any{"host": "web-1", "compare": true}))
	if sr := none.Hosts[0].Metrics[0]; sr.Previous != nil || sr.ChangeAvg != nil {
		t.Errorf("no previous data: %+v", sr)
	}
}

func TestHostMetricsComparePreviousOutsidePlan(t *testing.T) {
	f := newFake()
	f.points = points(10)
	f.prevOutside = true
	cs := connect(t, f, tokenA())
	out := decode[HostMetricsOut](t, call(t, cs, "host_metrics", map[string]any{"host": "web-1", "compare": true}))
	if out.Previous != nil || !out.Clamped || out.Hosts[0].Metrics[0].Previous != nil {
		t.Fatalf("out = %+v", out)
	}
}
