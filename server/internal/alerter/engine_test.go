package alerter

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type capturingSink struct {
	mu     sync.Mutex
	events []wire.Event
}

func (s *capturingSink) Publish(_ uuid.UUID, e wire.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *capturingSink) snapshot() []wire.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]wire.Event(nil), s.events...)
}

type nopStore struct{}

func (nopStore) WriteEvent(context.Context, clickhouse.EventRow) error { return nil }
func (nopStore) LastTints(context.Context, time.Time) ([]clickhouse.TintRow, error) {
	return nil, nil
}
func (nopStore) LastSeen(context.Context, time.Time) ([]clickhouse.NodeSeen, error) {
	return nil, nil
}

// fakeConfig gives the settings of each org (def otherwise) and the
// nodes.
type fakeConfig struct {
	mu       sync.Mutex
	def      store.ServerSettings
	settings map[uuid.UUID]store.ServerSettings
	nodes    map[uuid.UUID]store.Node
}

func newFakeConfig(def store.ServerSettings) *fakeConfig {
	return &fakeConfig{def: def, settings: map[uuid.UUID]store.ServerSettings{}, nodes: map[uuid.UUID]store.Node{}}
}

func (f *fakeConfig) GetSettings(_ context.Context, org uuid.UUID) (store.ServerSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.settings[org]; ok {
		return s, nil
	}
	return f.def, nil
}

func (f *fakeConfig) GetNode(_ context.Context, _, id uuid.UUID) (store.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nodes[id]
	if !ok {
		return store.Node{}, store.ErrNotFound
	}
	return n, nil
}

func TestAlerter_EscalatesImmediately(t *testing.T) {
	clock := &testClock{t: time.Unix(1700000000, 0).UTC()}
	sink := &capturingSink{}
	cfg := store.DefaultServerSettings()
	cfg.NotifyDebounceSeconds = 60

	e := New(nil, clock, sink, nopStore{}, newFakeConfig(cfg))
	org, node := uuid.New(), uuid.New()

	// Baseline OK sample.
	e.Evaluate(context.Background(), org, node, nil, 0.10, 0.20, 0.30)
	if len(sink.snapshot()) != 0 {
		t.Fatalf("expected no events on baseline, got %d", len(sink.snapshot()))
	}

	// Cross into critical directly — escalations fire without debounce.
	clock.advance(10 * time.Second)
	e.Evaluate(context.Background(), org, node, nil, 0.95, 0.10, 0.10)
	got := sink.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d (%+v)", len(got), got)
	}
	if got[0].Tint != string(TintCritical) || got[0].Metric != "cpu" {
		t.Errorf("unexpected event: %+v", got[0])
	}
}

func TestAlerter_DebouncesReturnToOK(t *testing.T) {
	clock := &testClock{t: time.Unix(1700000000, 0).UTC()}
	sink := &capturingSink{}
	cfg := store.DefaultServerSettings()
	cfg.NotifyDebounceSeconds = 60
	e := New(nil, clock, sink, nopStore{}, newFakeConfig(cfg))
	org, node := uuid.New(), uuid.New()

	// warn → critical transitions are immediate.
	e.Evaluate(context.Background(), org, node, nil, 0.10, 0.10, 0.10)
	e.Evaluate(context.Background(), org, node, nil, 0.95, 0.10, 0.10)
	if len(sink.snapshot()) != 1 {
		t.Fatalf("expected critical event first")
	}

	// Quick drop back to OK should be debounced.
	clock.advance(5 * time.Second)
	e.Evaluate(context.Background(), org, node, nil, 0.10, 0.10, 0.10)
	if len(sink.snapshot()) != 1 {
		t.Errorf("expected debounce to suppress OK transition, got %d", len(sink.snapshot()))
	}

	// After debounce window, OK fires.
	clock.advance(61 * time.Second)
	e.Evaluate(context.Background(), org, node, nil, 0.10, 0.10, 0.10)
	events := sink.snapshot()
	if len(events) != 2 {
		t.Fatalf("expected 2 events after debounce, got %d", len(events))
	}
	if events[1].Tint != string(TintOK) {
		t.Errorf("expected OK transition, got %+v", events[1])
	}
}

func TestAlerter_PerNodeThresholdsOverrideGlobal(t *testing.T) {
	clock := &testClock{t: time.Unix(1700000000, 0).UTC()}
	sink := &capturingSink{}
	cfg := store.DefaultServerSettings()
	cfg.NotifyDebounceSeconds = 60
	e := New(nil, clock, sink, nopStore{}, newFakeConfig(cfg))
	org, nodeID := uuid.New(), uuid.New()

	stricter := store.MetricThresholds{CPUWarn: 0.30, CPUCritical: 0.50, MemWarn: 0.90, MemCritical: 0.99, DiskWarn: 0.90, DiskCritical: 0.99}
	node := &store.Node{ID: nodeID, CustomThresholds: &stricter}

	// 0.35 is OK under defaults (warn 0.75) but warn under stricter (warn 0.30).
	e.Evaluate(context.Background(), org, nodeID, node, 0.10, 0.10, 0.10)
	e.Evaluate(context.Background(), org, nodeID, node, 0.35, 0.10, 0.10)
	events := sink.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Tint != string(TintWarn) || events[0].Metric != "cpu" {
		t.Errorf("expected CPU warn, got %+v", events[0])
	}
}

// runProcs sends one sample each minute, with the clock at the sample time.
func runProcs(e *Engine, clock *testClock, org, id uuid.UUID, node *store.Node, from, through int, items func(int) []clickhouse.ProcRow) {
	for m := from; m <= through; m++ {
		clock.mu.Lock()
		clock.t = lpAt(m)
		clock.mu.Unlock()
		e.EvaluateProcs(context.Background(), org, id, node, items(m), lpAt(m))
	}
}

func TestAlerter_LongProcEvents(t *testing.T) {
	clock := newClock()
	sink := &capturingSink{}
	st := &recordingStore{}
	e := New(nil, clock, sink, st, newFakeConfig(store.DefaultServerSettings()))
	org, id := uuid.New(), uuid.New()
	high := func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(85)} }

	runProcs(e, clock, org, id, nil, 0, 29, high)
	if got := sink.snapshot(); len(got) != 0 {
		t.Fatalf("before the sustain time: %+v", got)
	}
	runProcs(e, clock, org, id, nil, 30, 40, high)
	got := sink.snapshot()
	if len(got) != 1 || got[0].Kind != "long_process" || got[0].Metric != "long_proc" || got[0].Tint != string(TintWarn) {
		t.Fatalf("want one long_process warn event, got %+v", got)
	}
	runProcs(e, clock, org, id, nil, 41, 41, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{lpProc(1, 1, "sh", lpStarted)} })
	got = sink.snapshot()
	if len(got) != 2 || got[1].Kind != "long_process" || got[1].Tint != string(TintOK) {
		t.Fatalf("want an ok event on clear, got %+v", got)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.events) != 2 || st.events[0].Metric != "long_proc/812/1799000000" {
		t.Fatalf("events = %+v", st.events)
	}
	for _, key := range []string{`"pid":812`, `"name":"OrbStack Helper"`, `"cmd":"/usr/bin/OrbStack Helper"`, `"cpu_pct":85`, `"high_since":"2027-01-15T08:00:00Z"`, `"start_ts":"2027-01-03T18:13:20Z"`} {
		if !strings.Contains(st.events[0].Payload, key) {
			t.Errorf("payload %s has no %s", st.events[0].Payload, key)
		}
	}
}

func TestAlerter_LongProcNodeRuleAndIgnoreLists(t *testing.T) {
	cfg := store.DefaultServerSettings()
	cfg.LongProc.Ignore = []string{"postgres"}
	clock := newClock()
	sink := &capturingSink{}
	e := New(nil, clock, sink, nopStore{}, newFakeConfig(cfg))
	org := uuid.New()
	items := func(int) []clickhouse.ProcRow {
		return []clickhouse.ProcRow{
			lpProc(60, 1, "Postgres", lpStarted), lpProc(60, 2, "node", lpStarted),
			lpProc(60, 3, "OrbStack Helper", lpStarted), lpProc(30, 4, "java", lpStarted),
		}
	}

	// The org rule: postgres is ignored on all hosts, java is under 50%.
	a := uuid.New()
	nodeA := store.Node{ID: a, IgnoredProcs: []string{"NODE"}}
	runProcs(e, clock, org, a, &nodeA, 0, 30, items)
	if got := sink.snapshot(); len(got) != 1 {
		t.Fatalf("host A: want only OrbStack Helper, got %+v", got)
	}

	// A custom rule with a lower limit still uses the org ignore list.
	b := uuid.New()
	custom := store.LongProcRule{Enabled: true, CPUPct: 25, SustainSeconds: 1800, Notify: "all", Ignore: []string{"orbstack helper"}}
	nodeB := store.Node{ID: b, CustomLongProc: &custom}
	runProcs(e, clock, org, b, &nodeB, 31, 61, items)
	if got := sink.snapshot(); len(got) != 3 {
		t.Fatalf("host B: want node and java, got %+v", got[1:])
	}
}
