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
	regs  []clickhouse.NodeSeen
}

func (s restoreStore) LastTints(context.Context, time.Time) ([]clickhouse.TintRow, error) {
	return s.tints, nil
}

func (s restoreStore) LastSeen(context.Context, time.Time) ([]clickhouse.NodeSeen, error) {
	return s.seen, nil
}

func (s restoreStore) Registrations(context.Context, time.Time) ([]clickhouse.NodeSeen, error) {
	return s.regs, nil
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

func TestAlerter_LongProcNotify(t *testing.T) {
	clock := newClock()
	cfg := newFakeConfig(store.DefaultServerSettings())
	org, id := uuid.New(), uuid.New()
	node := notifyNode(id)
	n := newCountingNotifier()
	e := New(nil, clock, nil, nopStore{}, cfg, n)
	high := func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(85)} }
	gone := func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{lpProc(1, 1, "sh", lpStarted)} }

	runProcs(e, clock, org, id, &node, 0, 32, high)
	got := n.wait(1)
	if len(got) != 1 {
		t.Fatalf("want one alert, got %+v", got)
	}
	a := got[0]
	if a.Metric != "long_proc" || a.Instance != "812/1799000000" || a.To != TintWarn || a.Value != 85 {
		t.Fatalf("alert = %+v", a)
	}
	if a.Detail != "OrbStack Helper (pid 812) at 85% CPU for 30m." || a.Text() != a.Detail {
		t.Errorf("detail = %q, text = %q", a.Detail, a.Text())
	}
	if a.Title() != "web-1: long-running process" {
		t.Errorf("title = %q", a.Title())
	}

	// The clear sends an ok alert. A flag again inside the cooldown sends nothing.
	runProcs(e, clock, org, id, &node, 33, 33, gone)
	if got := n.wait(1); len(got) != 1 || got[0].To != TintOK {
		t.Fatalf("want one ok alert, got %+v", got)
	}
	runProcs(e, clock, org, id, &node, 34, 34, high)
	if got := n.wait(0); len(got) != 0 {
		t.Fatalf("cooldown: got %+v", got)
	}

	// A second process has its own cooldown.
	runProcs(e, clock, org, id, &node, 35, 70, func(int) []clickhouse.ProcRow {
		return []clickhouse.ProcRow{orb(85), lpProc(70, 9, "node", lpStarted)}
	})
	if got := n.wait(1); len(got) != 1 || got[0].Instance != "9/1799000000" {
		t.Fatalf("want one alert for node, got %+v", got)
	}
}

func TestAlerter_LongProcNotifyOff(t *testing.T) {
	clock := newClock()
	s := store.DefaultServerSettings()
	s.LongProc.Notify = "off"
	sink := &capturingSink{}
	org, id := uuid.New(), uuid.New()
	node := notifyNode(id)
	n := newCountingNotifier()
	e := New(nil, clock, sink, nopStore{}, newFakeConfig(s), n)
	runProcs(e, clock, org, id, &node, 0, 31, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(85)} })
	if got := n.wait(0); len(got) != 0 {
		t.Fatalf("notify off: got %+v", got)
	}
	if got := sink.snapshot(); len(got) != 1 {
		t.Fatalf("the event still goes to the clients: %+v", got)
	}
}

func TestAlerter_SweepDropsProcTracker(t *testing.T) {
	clock := newClock()
	cfg := newFakeConfig(store.DefaultServerSettings())
	org, id := uuid.New(), uuid.New()
	cfg.nodes[id] = notifyNode(id)
	node := cfg.nodes[id]
	sink := &capturingSink{}
	n := newCountingNotifier()
	e := New(nil, clock, sink, nopStore{}, cfg, n)
	ctx := context.Background()

	for m := 0; m <= 31; m++ {
		clock.mu.Lock()
		clock.t = lpAt(m)
		clock.mu.Unlock()
		e.Evaluate(ctx, org, id, &node, 0.1, 0.1, 0.1)
		e.EvaluateProcs(ctx, org, id, &node, []clickhouse.ProcRow{orb(85)}, lpAt(m))
	}
	if got := n.wait(1); len(got) != 1 || got[0].Metric != "long_proc" {
		t.Fatalf("want the long_proc alert, got %+v", got)
	}
	clock.advance(10 * time.Minute)
	e.Sweep(ctx)
	if got := n.wait(1); len(got) != 1 || got[0].Metric != "reachability" {
		t.Fatalf("want only the reachability alert, got %+v", got)
	}
	evs := sink.snapshot()
	if last := evs[len(evs)-1]; last.Kind != "long_process" || last.Tint != string(TintOK) {
		t.Fatalf("want a quiet long_process ok event, got %+v", evs)
	}
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if len(e.procs) != 0 {
		t.Fatal("the tracker is not dropped")
	}
}

// neverEnv is an engine with one enabled node and a counting notifier.
func neverEnv(t *testing.T, st Store) (*Engine, *testClock, *countingNotifier, uuid.UUID, uuid.UUID, store.Node) {
	t.Helper()
	clock := newClock()
	cfg := newFakeConfig(store.DefaultServerSettings())
	org, id := uuid.New(), uuid.New()
	cfg.nodes[id] = notifyNode(id)
	n := newCountingNotifier()
	return New(nil, clock, nil, st, cfg, n), clock, n, org, id, cfg.nodes[id]
}

func TestAlerter_NeverReported(t *testing.T) {
	st := &recordingStore{}
	e, clock, n, org, id, _ := neverEnv(t, st)
	ctx := context.Background()
	t0 := clock.Now()

	e.Expect(ctx, org, id, t0)
	clock.advance(9 * time.Minute)
	e.Sweep(ctx)
	if got := n.wait(0); len(got) != 0 {
		t.Fatalf("before 10m: got %+v", got)
	}
	clock.advance(time.Minute + time.Second)
	e.Sweep(ctx)
	e.Sweep(ctx) // a second sweep sends nothing new
	got := n.wait(1)
	if len(got) != 1 || got[0].Reason != "never_reported" || got[0].To != TintCritical || !got[0].Since.Equal(t0) {
		t.Fatalf("want one never_reported alert, got %+v", got)
	}
	if title := got[0].Title(); title != "web-1: never reported" {
		t.Errorf("title = %q", title)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.events) != 2 || st.events[0].Kind != "registered" || st.events[0].Tint != "ok" || st.events[1].Kind != "reachability_changed" {
		t.Fatalf("events = %+v", st.events)
	}
}

func TestAlerter_NeverReportedClearedBySample(t *testing.T) {
	e, clock, n, org, id, node := neverEnv(t, nopStore{})
	ctx := context.Background()

	e.Expect(ctx, org, id, clock.Now())
	clock.advance(2 * time.Minute)
	for range 18 {
		e.Evaluate(ctx, org, id, &node, 0.10, 0.10, 0.10)
		clock.advance(30 * time.Second)
	}
	e.Sweep(ctx)
	if got := n.wait(0); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAlerter_NeverReportedThenFirstSample(t *testing.T) {
	e, clock, n, org, id, node := neverEnv(t, nopStore{})
	ctx := context.Background()
	t0 := clock.Now()

	e.Expect(ctx, org, id, t0)
	clock.advance(11 * time.Minute)
	e.Sweep(ctx)
	if got := n.wait(1); len(got) != 1 {
		t.Fatalf("want the never-reported alert, got %+v", got)
	}
	clock.advance(2 * time.Minute)
	e.Evaluate(ctx, org, id, &node, 0.10, 0.10, 0.10)
	got := n.wait(1)
	if len(got) != 1 || got[0].To != TintOK || got[0].Value != 13*60 || !got[0].Since.Equal(t0) {
		t.Fatalf("want one reports-again alert with 780 s, got %+v", got)
	}
	if title := got[0].Title(); title != "web-1: reports again" {
		t.Errorf("title = %q", title)
	}
}

func TestAlerter_RecoveryCarriesDownTime(t *testing.T) {
	e, clock, n, org, id, node := neverEnv(t, nopStore{})
	ctx := context.Background()

	var last time.Time
	for range 4 {
		e.Evaluate(ctx, org, id, &node, 0.10, 0.10, 0.10)
		last = clock.Now()
		clock.advance(30 * time.Second)
	}
	// 30 s have passed since the last sample. The sweep comes in the gap.
	clock.advance(12*time.Minute + 10*time.Second)
	e.Sweep(ctx)
	got := n.wait(1)
	if len(got) != 1 || got[0].Reason != "stopped_reporting" || !got[0].Since.Equal(last) {
		t.Fatalf("want one stopped_reporting alert, got %+v", got)
	}
	e.Evaluate(ctx, org, id, &node, 0.10, 0.10, 0.10)
	got = n.wait(1)
	if len(got) != 1 || got[0].To != TintOK || got[0].Value != 760 || !got[0].Since.Equal(last) {
		t.Fatalf("want one ok alert with 760 s, got %+v", got)
	}
}

func TestAlerter_NeverReportedSkipsDeletedAndDisabled(t *testing.T) {
	clock := newClock()
	cfg := newFakeConfig(store.DefaultServerSettings())
	org, deleted, disabled := uuid.New(), uuid.New(), uuid.New()
	off := notifyNode(disabled)
	off.Enabled = false
	cfg.nodes[disabled] = off
	n := newCountingNotifier()
	e := New(nil, clock, nil, nopStore{}, cfg, n)
	ctx := context.Background()

	e.Expect(ctx, org, deleted, clock.Now())
	e.Expect(ctx, org, disabled, clock.Now())
	clock.advance(11 * time.Minute)
	e.Sweep(ctx)
	if got := n.wait(0); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if _, ok := e.expected[nodeKey{org: org, node: deleted}]; ok {
		t.Error("the deleted node stays in expected")
	}
}

func TestAlerter_LoadRestoresRegistrations(t *testing.T) {
	clock := newClock()
	cfg := newFakeConfig(store.DefaultServerSettings())
	org, id := uuid.New(), uuid.New()
	cfg.nodes[id] = notifyNode(id)
	st := restoreStore{regs: []clickhouse.NodeSeen{{OrgID: org, NodeID: id, TS: clock.Now()}}}
	n := newCountingNotifier()
	e := New(nil, clock, nil, st, cfg, n)
	ctx := context.Background()
	if err := e.Load(ctx); err != nil {
		t.Fatal(err)
	}
	clock.advance(10*time.Minute + time.Second)
	e.Sweep(ctx)
	if got := n.wait(1); len(got) != 1 || got[0].Reason != "never_reported" {
		t.Fatalf("got %+v", got)
	}
}

func TestAlerter_LoadSkipsAlertedRegistration(t *testing.T) {
	clock := newClock()
	cfg := newFakeConfig(store.DefaultServerSettings())
	org, id := uuid.New(), uuid.New()
	cfg.nodes[id] = notifyNode(id)
	reg := clock.Now()
	st := restoreStore{
		regs:  []clickhouse.NodeSeen{{OrgID: org, NodeID: id, TS: reg}},
		tints: []clickhouse.TintRow{{OrgID: org, NodeID: id, Metric: "reachability", Tint: "critical", TS: reg.Add(10 * time.Minute)}},
	}
	n := newCountingNotifier()
	node := cfg.nodes[id]
	e := New(nil, clock, nil, st, cfg, n)
	ctx := context.Background()
	if err := e.Load(ctx); err != nil {
		t.Fatal(err)
	}
	clock.advance(10*time.Minute + time.Second)
	e.Sweep(ctx)
	if got := n.wait(0); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	// The first sample still reports the down time from the registration.
	clock.advance(time.Minute)
	e.Evaluate(ctx, org, id, &node, 0.10, 0.10, 0.10)
	got := n.wait(1)
	if len(got) != 1 || got[0].To != TintOK || got[0].Value != 661 || !got[0].Since.Equal(reg) {
		t.Fatalf("got %+v", got)
	}
}
