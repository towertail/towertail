package alerter

import (
	"context"
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
