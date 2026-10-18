package alerter

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
)

// countingNotifier sends each alert on a channel.
type countingNotifier struct{ ch chan notify.Alert }

func newCountingNotifier() *countingNotifier {
	return &countingNotifier{ch: make(chan notify.Alert, 16)}
}

func (n *countingNotifier) Name() string { return "counting" }

func (n *countingNotifier) Notify(_ context.Context, a notify.Alert) error {
	n.ch <- a
	return nil
}

// wait gives the alerts that come in a short time.
func (n *countingNotifier) wait(want int) []notify.Alert {
	var got []notify.Alert
	timeout := time.After(200 * time.Millisecond)
	for {
		select {
		case a := <-n.ch:
			got = append(got, a)
			if len(got) > want {
				return got
			}
		case <-timeout:
			return got
		}
	}
}

func newClock() *testClock { return &testClock{t: time.Unix(1700000000, 0).UTC()} }

func notifyNode(id uuid.UUID) store.Node {
	return store.Node{ID: id, DisplayName: "web-1", Tags: []string{"prod"}, Enabled: true, NotifyOnWarn: true, NotifyOnCritical: true}
}

func TestAlerter_SettingsForEachOrg(t *testing.T) {
	cfg := newFakeConfig(store.DefaultServerSettings())
	orgA, orgB := uuid.New(), uuid.New()
	strict := store.DefaultServerSettings()
	strict.Thresholds.CPUWarn = 0.30
	cfg.settings[orgA] = strict
	sink := &capturingSink{}
	e := New(nil, newClock(), sink, nopStore{}, cfg)
	ctx := context.Background()

	for _, org := range []uuid.UUID{orgA, orgB} {
		node := uuid.New()
		e.Evaluate(ctx, org, node, nil, 0.10, 0.10, 0.10)
		e.Evaluate(ctx, org, node, nil, 0.40, 0.10, 0.10)
	}
	got := sink.snapshot()
	if len(got) != 1 || got[0].Tint != string(TintWarn) {
		t.Fatalf("want one warn event for org A, got %+v", got)
	}
}

func TestAlerter_InvalidateSettings(t *testing.T) {
	cfg := newFakeConfig(store.DefaultServerSettings())
	org, node := uuid.New(), uuid.New()
	sink := &capturingSink{}
	e := New(nil, newClock(), sink, nopStore{}, cfg)
	ctx := context.Background()

	e.Evaluate(ctx, org, node, nil, 0.40, 0.10, 0.10)
	strict := store.DefaultServerSettings()
	strict.Thresholds.CPUWarn = 0.30
	cfg.mu.Lock()
	cfg.settings[org] = strict
	cfg.mu.Unlock()
	// The cache keeps the old thresholds until the org is invalidated.
	e.Evaluate(ctx, org, node, nil, 0.40, 0.10, 0.10)
	if n := len(sink.snapshot()); n != 0 {
		t.Fatalf("cached settings: got %d events", n)
	}
	e.InvalidateSettings(org)
	e.Evaluate(ctx, org, node, nil, 0.40, 0.10, 0.10)
	if n := len(sink.snapshot()); n != 1 {
		t.Fatalf("after invalidate: got %d events, want 1", n)
	}
}

func TestAlerter_NotifyRules(t *testing.T) {
	future := time.Unix(1700000000, 0).Add(time.Hour)
	for _, tc := range []struct {
		name     string
		settings func(*store.ServerSettings)
		node     func(*store.Node)
		want     int
	}{
		{name: "sends", want: 1},
		{name: "org notifications off", settings: func(s *store.ServerSettings) { s.NotificationsEnabled = false }},
		{name: "org critical off", settings: func(s *store.ServerSettings) { s.NotifyCritical = false }},
		{name: "node critical off", node: func(n *store.Node) { n.NotifyOnCritical = false }},
		{name: "node snoozed", node: func(n *store.Node) { n.SnoozedUntil = &future }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := store.DefaultServerSettings()
			if tc.settings != nil {
				tc.settings(&s)
			}
			org, id := uuid.New(), uuid.New()
			node := notifyNode(id)
			if tc.node != nil {
				tc.node(&node)
			}
			n := newCountingNotifier()
			e := New(nil, newClock(), nil, nopStore{}, newFakeConfig(s), n)
			ctx := context.Background()
			e.Evaluate(ctx, org, id, &node, 0.10, 0.10, 0.10)
			e.Evaluate(ctx, org, id, &node, 0.95, 0.10, 0.10)
			got := n.wait(tc.want)
			if len(got) != tc.want {
				t.Fatalf("got %d alerts, want %d", len(got), tc.want)
			}
			if tc.want == 1 {
				a := got[0]
				if a.OrgID != org || a.NodeID != id || a.NodeName != "web-1" || a.Metric != "cpu" ||
					a.From != TintOK || a.To != TintCritical || a.Value != 0.95 ||
					a.EventID == uuid.Nil || len(a.Tags) != 1 || a.Tags[0] != "prod" {
					t.Fatalf("alert = %+v", a)
				}
			}
		})
	}
}

func TestAlerter_Cooldown(t *testing.T) {
	s := store.DefaultServerSettings()
	s.NotifyDebounceSeconds = 0
	s.NotifyCooldownSeconds = 600
	clock := newClock()
	n := newCountingNotifier()
	e := New(nil, clock, nil, nopStore{}, newFakeConfig(s), n)
	org, id := uuid.New(), uuid.New()
	node := notifyNode(id)
	ctx := context.Background()

	e.Evaluate(ctx, org, id, &node, 0.10, 0.10, 0.10)
	flap := func() {
		clock.advance(time.Minute)
		e.Evaluate(ctx, org, id, &node, 0.95, 0.10, 0.10)
		clock.advance(time.Minute)
		e.Evaluate(ctx, org, id, &node, 0.10, 0.10, 0.10)
	}
	flap()
	// critical and the recovery.
	if got := n.wait(2); len(got) != 2 {
		t.Fatalf("first flap: got %d alerts, want 2", len(got))
	}
	flap()
	if got := n.wait(0); len(got) != 0 {
		t.Fatalf("flap in cooldown: got %d alerts, want 0", len(got))
	}
	clock.advance(10 * time.Minute)
	flap()
	if got := n.wait(2); len(got) != 2 {
		t.Fatalf("flap after cooldown: got %d alerts, want 2", len(got))
	}
}

// restoreStore gives a fixed state to Load.
type restoreStore struct {
	nopStore
	tints []clickhouse.TintRow
	seen  []clickhouse.NodeSeen
}

func (s restoreStore) LastTints(context.Context, time.Time) ([]clickhouse.TintRow, error) {
	return s.tints, nil
}

func (s restoreStore) LastSeen(context.Context, time.Time) ([]clickhouse.NodeSeen, error) {
	return s.seen, nil
}

func TestAlerter_RestartSendsNoDuplicate(t *testing.T) {
	clock := newClock()
	org, id := uuid.New(), uuid.New()
	st := restoreStore{
		tints: []clickhouse.TintRow{{OrgID: org, NodeID: id, Metric: "cpu", Tint: "critical", TS: clock.Now().Add(-time.Hour)}},
		seen:  []clickhouse.NodeSeen{{OrgID: org, NodeID: id, TS: clock.Now().Add(-5 * time.Second)}},
	}
	sink := &capturingSink{}
	n := newCountingNotifier()
	node := notifyNode(id)
	e := New(nil, clock, sink, st, newFakeConfig(store.DefaultServerSettings()), n)
	if err := e.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Evaluate(context.Background(), org, id, &node, 0.95, 0.10, 0.10)
	if got := sink.snapshot(); len(got) != 0 {
		t.Fatalf("got events after restart: %+v", got)
	}
	if got := n.wait(0); len(got) != 0 {
		t.Fatalf("got alerts after restart: %+v", got)
	}
	// The restored state is still live: a drop to ok is a change.
	clock.advance(2 * time.Minute)
	e.Evaluate(context.Background(), org, id, &node, 0.10, 0.10, 0.10)
	if got := sink.snapshot(); len(got) != 1 || got[0].Tint != string(TintOK) {
		t.Fatalf("want one ok event, got %+v", got)
	}
}

// recordingStore records the events.
type recordingStore struct {
	nopStore
	mu     sync.Mutex
	events []clickhouse.EventRow
}

func (s *recordingStore) WriteEvent(_ context.Context, e clickhouse.EventRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

func TestAlerter_ReachabilitySweep(t *testing.T) {
	clock := newClock()
	cfg := newFakeConfig(store.DefaultServerSettings())
	org, id := uuid.New(), uuid.New()
	cfg.nodes[id] = notifyNode(id)
	node := cfg.nodes[id]
	st := &recordingStore{}
	n := newCountingNotifier()
	e := New(nil, clock, nil, st, cfg, n)
	ctx := context.Background()

	e.Evaluate(ctx, org, id, &node, 0.10, 0.10, 0.10)
	clock.advance(10 * time.Second)
	e.Evaluate(ctx, org, id, &node, 0.10, 0.10, 0.10)

	clock.advance(25 * time.Second)
	e.Sweep(ctx)
	if got := n.wait(0); len(got) != 0 {
		t.Fatalf("before 3 intervals: got %+v", got)
	}

	clock.advance(10 * time.Second)
	e.Sweep(ctx)
	e.Sweep(ctx) // a second sweep sends nothing new
	got := n.wait(1)
	if len(got) != 1 || got[0].Metric != "reachability" || got[0].To != TintCritical {
		t.Fatalf("want one reachability critical alert, got %+v", got)
	}

	clock.advance(time.Second)
	e.Evaluate(ctx, org, id, &node, 0.10, 0.10, 0.10)
	got = n.wait(1)
	if len(got) != 1 || got[0].Metric != "reachability" || got[0].To != TintOK {
		t.Fatalf("want one reachability ok alert, got %+v", got)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.events) != 2 || st.events[0].Kind != "reachability_changed" {
		t.Fatalf("events = %+v", st.events)
	}
}

func TestAlerter_SweepSkipsDeletedAndDisabledNodes(t *testing.T) {
	clock := newClock()
	cfg := newFakeConfig(store.DefaultServerSettings())
	org, deleted, disabled := uuid.New(), uuid.New(), uuid.New()
	off := notifyNode(disabled)
	off.Enabled = false
	cfg.nodes[disabled] = off
	n := newCountingNotifier()
	e := New(nil, clock, nil, nopStore{}, cfg, n)
	ctx := context.Background()

	e.Evaluate(ctx, org, deleted, nil, 0.10, 0.10, 0.10)
	e.Evaluate(ctx, org, disabled, &off, 0.10, 0.10, 0.10)
	clock.advance(time.Minute)
	e.Sweep(ctx)
	if got := n.wait(0); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}
