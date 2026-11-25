package alerter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/sampler/pkg/schema"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// rulesEnv runs samples with a fake clock.
type rulesEnv struct {
	e     *Engine
	clock *testClock
	sink  *capturingSink
	st    *recordingStore
	n     *countingNotifier
	cfg   *fakeConfig
	org   uuid.UUID
	id    uuid.UUID
	node  store.Node
}

func newRulesEnv(s store.ServerSettings) *rulesEnv {
	v := &rulesEnv{clock: newClock(), sink: &capturingSink{}, st: &recordingStore{}, n: newCountingNotifier(),
		cfg: newFakeConfig(s), org: uuid.New(), id: uuid.New()}
	v.node = notifyNode(v.id)
	v.cfg.nodes[v.id] = v.node
	v.e = New(nil, v.clock, v.sink, v.st, v.cfg, v.n)
	return v
}

// feed sends n samples, one each step. The first one comes at the
// current time.
func (v *rulesEnv) feed(r Reading, step time.Duration, n int) {
	for i := range n {
		if i > 0 {
			v.clock.advance(step)
		}
		v.e.Evaluate(context.Background(), v.org, v.id, &v.node, r)
	}
}

func quiet() Reading { return Reading{CPU: 0.1, Mem: 0.1, Disk: 0.1} }

func (v *rulesEnv) events(metric string) []wire.Event {
	var out []wire.Event
	for _, e := range v.sink.snapshot() {
		if e.Metric == metric {
			out = append(out, e)
		}
	}
	return out
}

func sustained() store.ServerSettings {
	s := instantSettings()
	s.NotifyDebounceSeconds = 0
	s.Alerts.CPU = store.AlertRule{SustainSeconds: 300, Notify: store.NotifyAll}
	s.Alerts.Health = store.AlertRule{SustainSeconds: 300, Notify: store.NotifyAll}
	return s
}

func TestAlerter_SustainDelaysEscalation(t *testing.T) {
	v := newRulesEnv(sustained())
	hot := Reading{CPU: 0.95, Mem: 0.1, Disk: 0.1}
	v.feed(hot, 10*time.Second, 25) // 0..240 s
	if got := v.events("cpu"); len(got) != 0 {
		t.Fatalf("after 4 min: %+v", got)
	}
	v.feed(hot, 10*time.Second, 7) // up to 300 s
	got := v.events("cpu")
	if len(got) != 1 || got[0].Tint != "critical" || got[0].Kind != "threshold_crossed" {
		t.Fatalf("after 5 min: %+v", got)
	}
}

func TestAlerter_ShortSpikeDoesNotAlert(t *testing.T) {
	v := newRulesEnv(sustained())
	v.feed(quiet(), 10*time.Second, 10)
	v.feed(Reading{CPU: 0.95, Mem: 0.1, Disk: 0.1}, 10*time.Second, 12)
	v.feed(quiet(), 10*time.Second, 40)
	if got := v.events("cpu"); len(got) != 0 {
		t.Fatalf("events: %+v", got)
	}
}

func TestAlerter_ZeroSustainEscalatesAtOnce(t *testing.T) {
	s := store.DefaultServerSettings() // disk sustain is 0
	v := newRulesEnv(s)
	v.feed(quiet(), time.Second, 1)
	v.feed(Reading{CPU: 0.1, Mem: 0.1, Disk: 0.97}, time.Second, 2)
	got := v.events("disk")
	if len(got) != 1 || got[0].Tint != "critical" {
		t.Fatalf("disk events: %+v", got)
	}
}

func TestAlerter_PerMetricOverride(t *testing.T) {
	v := newRulesEnv(instantSettings())
	v.node.ThresholdOverrides = &store.ThresholdOverrides{Mem: &store.ThresholdPair{Warn: 0.5, Critical: 0.6}}
	v.feed(quiet(), time.Second, 1)
	v.feed(Reading{CPU: 0.5, Mem: 0.55, Disk: 0.1}, time.Second, 2)
	if got := v.events("mem"); len(got) != 1 || got[0].Tint != "warn" {
		t.Fatalf("mem events: %+v", got)
	}
	if got := v.events("cpu"); len(got) != 0 {
		t.Fatalf("cpu uses the org values: %+v", got)
	}
}

func TestAlerter_MuteResolvesQuietly(t *testing.T) {
	v := newRulesEnv(instantSettings())
	v.feed(quiet(), time.Second, 1)
	v.feed(Reading{CPU: 0.95, Mem: 0.1, Disk: 0.1}, time.Second, 2)
	if got := v.n.wait(1); len(got) != 1 {
		t.Fatalf("critical alert: %d", len(got))
	}
	v.node.MutedSignals = store.MutedSignals{store.MetricCPU}
	v.feed(Reading{CPU: 0.95, Mem: 0.1, Disk: 0.1}, time.Second, 2)
	got := v.events("cpu")
	if len(got) != 2 || got[1].Tint != "ok" || got[1].Notify {
		t.Fatalf("events: %+v", got)
	}
	if got := v.n.wait(0); len(got) != 0 {
		t.Fatalf("a mute sent %d notifications", len(got))
	}
	var found bool
	for _, e := range v.st.events {
		found = found || (e.Metric == "cpu" && e.Tint == "ok" && strings.Contains(e.Payload, `"reason":"muted"`))
	}
	if !found {
		t.Fatalf("no stored event with reason muted: %+v", v.st.events)
	}
}

func TestAlerter_OrgMuteAppliesToAllHosts(t *testing.T) {
	s := instantSettings()
	s.Muted = store.MutedSignals{store.MetricDisk}
	v := newRulesEnv(s)
	other := uuid.New()
	for _, id := range []uuid.UUID{v.id, other} {
		node := notifyNode(id)
		v.e.Evaluate(context.Background(), v.org, id, &node, quiet())
		v.e.Evaluate(context.Background(), v.org, id, &node, Reading{CPU: 0.1, Mem: 0.1, Disk: 0.99})
	}
	if got := v.events("disk"); len(got) != 0 {
		t.Fatalf("disk events: %+v", got)
	}
}

func brief(mod func(*wire.HealthBrief)) *wire.HealthBrief {
	h := &wire.HealthBrief{Procs: 100}
	mod(h)
	return h
}

func TestAlerter_HealthAlert(t *testing.T) {
	v := newRulesEnv(sustained())
	r := quiet()
	r.Health = brief(func(h *wire.HealthBrief) { h.PSI = &schema.PSIInfo{MemSome: 50, MemFull: 25} })
	v.feed(r, 10*time.Second, 30) // 0..290 s
	if got := v.events("health"); len(got) != 0 {
		t.Fatalf("before 5 min: %+v", got)
	}
	v.feed(r, 10*time.Second, 2)
	got := v.events("health")
	if len(got) != 1 || got[0].Tint != "critical" || !strings.Contains(got[0].Text, "Memory pressure 25%") || !got[0].Notify {
		t.Fatalf("health events: %+v", got)
	}
	// The checks clear for a full window, then the alert closes.
	r.Health = brief(func(*wire.HealthBrief) {})
	v.feed(r, 10*time.Second, 40)
	got = v.events("health")
	if len(got) != 2 || got[1].Tint != "ok" {
		t.Fatalf("health events after recovery: %+v", got)
	}
}

func TestAlerter_InodesAlertAtOnce(t *testing.T) {
	v := newRulesEnv(sustained())
	r := quiet()
	r.Health = brief(func(*wire.HealthBrief) {})
	v.feed(r, 10*time.Second, 2)
	r.Health = brief(func(h *wire.HealthBrief) { h.Inodes = 0.97 })
	v.feed(r, 10*time.Second, 2)
	got := v.events("health")
	if len(got) != 1 || got[0].Tint != "critical" || !strings.Contains(got[0].Text, "Inodes 97%") {
		t.Fatalf("health events: %+v", got)
	}
}

func TestAlerter_NoHealthKeepsState(t *testing.T) {
	s := sustained()
	s.Alerts.Health.SustainSeconds = 0
	v := newRulesEnv(s)
	r := quiet()
	r.Health = brief(func(*wire.HealthBrief) {})
	v.feed(r, time.Second, 1)
	r.Health = brief(func(h *wire.HealthBrief) { h.Zombies = ptr(5000) })
	v.feed(r, time.Second, 2)
	if got := v.events("health"); len(got) != 1 {
		t.Fatalf("health events: %+v", got)
	}
	r.Health = nil
	v.feed(r, time.Second, 200)
	if got := v.events("health"); len(got) != 1 {
		t.Fatalf("a sample with no health changed the state: %+v", got)
	}
}

func ptr[T any](v T) *T { return &v }

func TestAlerter_HealthMuteClosesQuietly(t *testing.T) {
	s := sustained()
	s.Alerts.Health.SustainSeconds = 0
	v := newRulesEnv(s)
	r := quiet()
	r.Health = brief(func(h *wire.HealthBrief) { h.Zombies = ptr(5000) })
	ok := quiet()
	ok.Health = brief(func(*wire.HealthBrief) {})
	v.feed(ok, time.Second, 1)
	v.feed(r, time.Second, 3)
	if got := v.events("health"); len(got) != 1 || got[0].Tint != "critical" {
		t.Fatalf("health events: %+v", got)
	}
	v.n.wait(1)
	v.node.MutedSignals = store.MutedSignals{store.MetricZombies}
	v.feed(r, time.Second, 2)
	got := v.events("health")
	if len(got) != 2 || got[1].Tint != "ok" || got[1].Notify {
		t.Fatalf("health events after mute: %+v", got)
	}
}

// restoredStore gives one open alert to Load.
type restoredStore struct {
	nopStore
	rows []clickhouse.TintRow
}

func (s restoredStore) LastTints(context.Context, time.Time) ([]clickhouse.TintRow, error) {
	return s.rows, nil
}

func TestAlerter_RestartKeepsOpenAlertDuringSustainWarmup(t *testing.T) {
	clock := newClock()
	sink := &capturingSink{}
	org, id := uuid.New(), uuid.New()
	st := restoredStore{rows: []clickhouse.TintRow{{
		OrgID: org, NodeID: id, Metric: "cpu", Tint: "critical", TS: clock.Now().Add(-time.Hour), AlertID: uuid.New(),
	}}}
	e := New(nil, clock, sink, st, newFakeConfig(sustained()))
	if err := e.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	node := notifyNode(id)
	for i := range 20 { // 190 s of ok samples
		if i > 0 {
			clock.advance(10 * time.Second)
		}
		e.Evaluate(context.Background(), org, id, &node, quiet())
	}
	if got := sink.snapshot(); len(got) != 0 {
		t.Fatalf("events during warm-up: %+v", got)
	}
	for range 12 {
		clock.advance(10 * time.Second)
		e.Evaluate(context.Background(), org, id, &node, quiet())
	}
	if got := sink.snapshot(); len(got) != 1 || got[0].Tint != "ok" {
		t.Fatalf("events after warm-up: %+v", got)
	}
}

func TestAlerter_SampleTimeDrivesWindow(t *testing.T) {
	v := newRulesEnv(sustained())
	hot := Reading{CPU: 0.95, Mem: 0.1, Disk: 0.1}
	// All samples come now, but their times span 5 min.
	for i := range 31 {
		hot.At = v.clock.Now().Add(time.Duration(i) * 10 * time.Second)
		v.e.Evaluate(context.Background(), v.org, v.id, &v.node, hot)
	}
	if got := v.events("cpu"); len(got) != 1 {
		t.Fatalf("cpu events: %+v", got)
	}
}
