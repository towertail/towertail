package query

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
)

// minuteSeries gives one point each minute for 2 hours that end at now.
// The run from index from to index to has the value hi, the rest lo.
func minuteSeries(lo, hi float64, from, to int) []clickhouse.HistoryPoint {
	pts := make([]clickhouse.HistoryPoint, 120)
	for i := range pts {
		v := lo
		if i >= from && i < to {
			v = hi
		}
		pts[i] = clickhouse.HistoryPoint{TS: now.Add(time.Duration(i-120) * time.Minute), Value: v}
	}
	return pts
}

func suggestRequest(node uuid.UUID, metric store.ThresholdMetric) SuggestRequest {
	return SuggestRequest{NodeID: node, Metric: metric, From: now.Add(-2 * time.Hour)}
}

func TestSuggestThresholds(t *testing.T) {
	r := &fakeReader{
		minute: map[uuid.UUID]map[string][]clickhouse.HistoryPoint{hostA: {"cpu": minuteSeries(10, 80, 40, 60)}},
		incidents: []clickhouse.IncidentRow{
			incident(uuid.New(), hostA, "threshold_crossed", "cpu", "warn", 1, nil),
			incident(uuid.New(), hostA, "threshold_crossed", "mem", "warn", 1, nil),
		},
	}
	s := newService(r, []store.Node{{ID: hostA, DisplayName: "web-1"}}, 0)
	req := suggestRequest(hostA, store.MetricCPU)
	req.Candidates = []store.ThresholdPair{{Warn: 0.85, Critical: 0.95}}
	got, err := s.SuggestThresholds(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Approximate || got.Resolution != time.Minute || got.Hosts != 1 || got.ActualAlerts != 1 {
		t.Fatalf("suggestion = %+v", got)
	}
	if len(r.gotMinute) != 1 || len(r.gotMinute[0].NodeIDs) != 1 || r.gotMinute[0].Metrics[0] != "cpu" {
		t.Fatalf("read = %+v", r.gotMinute)
	}
	if got.Percentiles["max"] != 0.8 || got.Percentiles["p50"] != 0.1 {
		t.Errorf("cpu percent must become a ratio: %v", got.Percentiles)
	}
	cur := got.Candidates[0]
	if !cur.Current || cur.Pair != (store.ThresholdPair{Warn: 0.75, Critical: 0.9}) || cur.Count.Alerts != 1 || cur.SustainSeconds != 300 {
		t.Fatalf("current = %+v", cur)
	}
	if cur.Count.Notifications != 0 {
		t.Errorf("notify critical: a warn alert gives no notification: %+v", cur.Count)
	}
	high := got.Candidates[1]
	if high.Current || high.Pair.Warn != 0.85 || high.Count.Alerts != 0 {
		t.Errorf("a higher candidate must give fewer alerts: %+v", high)
	}
	if len(got.Candidates) != 3 || got.Candidates[2].Pair != (store.ThresholdPair{Warn: 0.8, Critical: 0.8}) {
		t.Errorf("candidates = %+v, want current, asked and one pair from the percentiles", got.Candidates)
	}
}

func TestSuggestThresholdsSustain(t *testing.T) {
	r := &fakeReader{minute: map[uuid.UUID]map[string][]clickhouse.HistoryPoint{hostA: {"cpu": minuteSeries(10, 80, 40, 45)}}}
	s := newService(r, []store.Node{{ID: hostA}}, 0)
	req := suggestRequest(hostA, store.MetricCPU)
	zero := 0
	req.SustainSeconds = &zero
	got, err := s.SuggestThresholds(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if c := got.Candidates[0]; c.SustainSeconds != 0 || c.Count.Alerts != 1 {
		t.Fatalf("5 min over warn with sustain 0: %+v", c)
	}
	got, err = s.SuggestThresholds(context.Background(), suggestRequest(hostA, store.MetricCPU))
	if err != nil {
		t.Fatal(err)
	}
	if c := got.Candidates[0]; c.SustainSeconds != 300 {
		t.Fatalf("default sustain: %+v", c)
	}
}

func TestSuggestThresholdsErrors(t *testing.T) {
	s := newService(&fakeReader{}, []store.Node{{ID: hostA}}, 0)
	ctx := context.Background()
	req := suggestRequest(hostA, "procs")
	if _, err := s.SuggestThresholds(ctx, req); !errors.Is(err, ErrBadMetric) {
		t.Errorf("procs: err = %v", err)
	}
	req = suggestRequest(hostA, store.MetricCPU)
	req.From = now.Add(-31 * 24 * time.Hour)
	if _, err := s.SuggestThresholds(ctx, req); !errors.Is(err, ErrBadRange) {
		t.Errorf("31 d: err = %v", err)
	}
	off := newService(nil, []store.Node{{ID: hostA}}, 0)
	if _, err := off.SuggestThresholds(ctx, suggestRequest(hostA, store.MetricCPU)); !errors.Is(err, ErrNoHistory) {
		t.Errorf("no history: err = %v", err)
	}
}

func TestSuggestThresholdsUnknownNode(t *testing.T) {
	s := newService(&fakeReader{}, []store.Node{{ID: hostA}}, 0)
	if _, err := s.SuggestThresholds(context.Background(), suggestRequest(uuid.New(), store.MetricCPU)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// With no node, the hosts on the org limits are tested. A host with its
// own pair is left out. More than 50 hosts are cut.
func TestSuggestThresholdsOrgHosts(t *testing.T) {
	own := uuid.New()
	nodes := []store.Node{{ID: own, DisplayName: "own", ThresholdOverrides: &store.ThresholdOverrides{CPU: &store.ThresholdPair{Warn: 0.5, Critical: 0.6}}}}
	series := map[uuid.UUID]map[string][]clickhouse.HistoryPoint{}
	for i := range 60 {
		id := uuid.New()
		nodes = append(nodes, store.Node{ID: id, DisplayName: fmt.Sprintf("h%02d", i)})
		series[id] = map[string][]clickhouse.HistoryPoint{"cpu": minuteSeries(10, 80, 40, 60)}
	}
	r := &fakeReader{minute: series}
	s := newService(r, nodes, 0)
	got, err := s.SuggestThresholds(context.Background(), suggestRequest(uuid.Nil, store.MetricCPU))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || got.Hosts != 50 || len(r.gotMinute[0].NodeIDs) != 50 {
		t.Fatalf("hosts = %d, truncated = %v", got.Hosts, got.Truncated)
	}
	for _, id := range r.gotMinute[0].NodeIDs {
		if id == own {
			t.Fatal("a host with its own pair must not be tested")
		}
	}
	if c := got.Candidates[0]; c.HostsAlerting != 50 || c.Count.Alerts != 50 {
		t.Errorf("current = %+v", c)
	}
}

func TestSuggestThresholdsClamp(t *testing.T) {
	r := &fakeReader{minute: map[uuid.UUID]map[string][]clickhouse.HistoryPoint{hostA: {"mem": minuteSeries(0.2, 0.9, 40, 60)}}}
	s := newService(r, []store.Node{{ID: hostA}}, 1)
	req := suggestRequest(hostA, store.MetricMem)
	req.From = now.Add(-72 * time.Hour)
	got, err := s.SuggestThresholds(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Clamped || !got.From.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("clamp: %+v", got)
	}
	if got.Percentiles["max"] != 0.9 {
		t.Errorf("mem is a ratio already: %v", got.Percentiles)
	}
}
