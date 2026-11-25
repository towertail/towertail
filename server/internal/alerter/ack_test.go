package alerter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
)

// ackEnv is an engine with one host and a counting notifier.
type ackEnv struct {
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

func newAckEnv(t *testing.T) *ackEnv {
	t.Helper()
	s := store.DefaultServerSettings()
	s.NotifyDebounceSeconds = 0
	s.NotifyCooldownSeconds = 1
	env := &ackEnv{clock: newClock(), sink: &capturingSink{}, st: &recordingStore{}, n: newCountingNotifier(), cfg: newFakeConfig(s), org: uuid.New(), id: uuid.New()}
	env.node = notifyNode(env.id)
	env.cfg.nodes[env.id] = env.node
	env.e = New(nil, env.clock, env.sink, env.st, env.cfg, env.n)
	return env
}

// cpu runs one sample with the CPU ratio after the clock moves.
func (v *ackEnv) cpu(ratio float64) {
	v.clock.advance(time.Minute)
	v.e.Evaluate(context.Background(), v.org, v.id, &v.node, ratio, 0.10, 0.10)
}

func (v *ackEnv) ack(alert uuid.UUID, mod func(*notify.Ack)) {
	a := notify.Ack{OrgID: v.org, AlertID: alert, EventID: alert, NodeID: v.id, Metric: "cpu", By: uuid.New(), At: v.clock.Now()}
	if mod != nil {
		mod(&a)
	}
	v.e.Acked(context.Background(), a)
}

// open runs an ok sample and a warn sample, and gives the alert id.
func (v *ackEnv) open(t *testing.T) uuid.UUID {
	t.Helper()
	v.cpu(0.10)
	v.cpu(0.80)
	got := v.n.wait(1)
	if len(got) != 1 || got[0].AlertID == uuid.Nil {
		t.Fatalf("open: alerts = %+v", got)
	}
	return got[0].AlertID
}

func TestAlerter_AckStopsNotifications(t *testing.T) {
	v := newAckEnv(t)
	id := v.open(t)
	v.ack(id, nil)
	v.cpu(0.95)
	if got := v.n.wait(0); len(got) != 0 {
		t.Fatalf("critical after the ack: %+v", got)
	}
	events := v.sink.snapshot()
	if len(events) != 2 || events[1].Notify {
		t.Fatalf("events = %+v, want a critical event with no notify", events)
	}
	v.cpu(0.10)
	if got := v.n.wait(1); len(got) != 1 || got[0].To != TintOK {
		t.Fatalf("recovery after the ack: %+v", got)
	}
}

func TestAlerter_SnoozeStopsUntil(t *testing.T) {
	v := newAckEnv(t)
	id := v.open(t)
	until := v.clock.Now().Add(10 * time.Minute)
	v.ack(id, func(a *notify.Ack) { a.Snooze, a.Until = true, &until })
	v.cpu(0.95)
	v.cpu(0.10)
	if got := v.n.wait(0); len(got) != 0 {
		t.Fatalf("alerts in the snooze: %+v", got)
	}
	v.clock.advance(15 * time.Minute)
	v.cpu(0.80)
	if got := v.n.wait(1); len(got) != 1 {
		t.Fatalf("after the snooze: %+v", got)
	}
}

func TestAlerter_SnoozeEndReminds(t *testing.T) {
	v := newAckEnv(t)
	id := v.open(t)
	until := v.clock.Now().Add(10 * time.Minute)
	v.ack(id, func(a *notify.Ack) { a.Snooze, a.Until = true, &until })
	v.cpu(0.95)
	v.clock.advance(11 * time.Minute)
	v.e.wakeSnoozed(context.Background(), v.clock.Now())
	v.e.wakeSnoozed(context.Background(), v.clock.Now())
	got := v.n.wait(1)
	if len(got) != 1 || !got[0].Repeat || got[0].AlertID != id {
		t.Fatalf("reminder = %+v", got)
	}
	if !strings.Contains(got[0].Text(), "still") {
		t.Errorf("text = %q", got[0].Text())
	}
}

func TestAlerter_UndoAck(t *testing.T) {
	v := newAckEnv(t)
	id := v.open(t)
	v.ack(id, nil)
	v.ack(id, func(a *notify.Ack) { a.Undo = true })
	v.cpu(0.95)
	if got := v.n.wait(1); len(got) != 1 || got[0].To != TintCritical {
		t.Fatalf("after the undo: %+v", got)
	}
	until := v.clock.Now().Add(time.Hour)
	v.ack(id, func(a *notify.Ack) { a.Snooze, a.Until = true, &until })
	v.ack(id, func(a *notify.Ack) { a.Snooze, a.Undo = true, true })
	v.cpu(0.80)
	if got := v.n.wait(1); len(got) != 1 {
		t.Fatalf("after the unsnooze: %+v", got)
	}
}

func TestAlerter_AckOtherAlertID(t *testing.T) {
	v := newAckEnv(t)
	v.open(t)
	v.ack(uuid.New(), nil)
	v.cpu(0.95)
	if got := v.n.wait(1); len(got) != 1 {
		t.Fatalf("an ack of another alert must not stop this one: %+v", got)
	}
}

func TestAlerter_AlertIDStable(t *testing.T) {
	v := newAckEnv(t)
	v.cpu(0.10)
	v.cpu(0.80)
	v.cpu(0.95)
	v.cpu(0.10)
	v.cpu(0.80)
	ev := v.sink.snapshot()
	if len(ev) != 4 {
		t.Fatalf("events = %+v", ev)
	}
	if ev[0].AlertID != ev[0].ID {
		t.Errorf("the id of the alert is the id of its first event: %+v", ev[0])
	}
	if ev[1].AlertID != ev[0].AlertID || ev[2].AlertID != ev[0].AlertID {
		t.Errorf("one incident must keep one alert id: %+v", ev)
	}
	if ev[3].AlertID == ev[0].AlertID || ev[3].AlertID != ev[3].ID {
		t.Errorf("a new incident needs a new alert id: %+v", ev)
	}
}

func TestAlerter_EventNotifyFlag(t *testing.T) {
	v := newAckEnv(t)
	future := v.clock.Now().Add(time.Hour)
	v.node.SnoozedUntil = &future
	v.cpu(0.10)
	v.cpu(0.80)
	ev := v.sink.snapshot()
	if len(ev) != 1 || ev[0].Notify || ev[0].Title == "" || ev[0].Text == "" {
		t.Fatalf("snoozed host: %+v", ev)
	}
	v.node.SnoozedUntil = nil
	v.cpu(0.95)
	ev = v.sink.snapshot()
	if len(ev) != 2 || !ev[1].Notify {
		t.Fatalf("events = %+v, want notify on the second", ev)
	}
}

// marksStore gives a fixed state and marks to Load.
type marksStore struct {
	restoreStore
	marks []clickhouse.AlertMark
}

func (s marksStore) OpenMarks(context.Context, time.Time) ([]clickhouse.AlertMark, error) {
	return s.marks, nil
}

func TestAlerter_LoadRestoresAck(t *testing.T) {
	clock := newClock()
	org, id, alert, by := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	st := marksStore{
		restoreStore: restoreStore{
			tints: []clickhouse.TintRow{{OrgID: org, NodeID: id, AlertID: alert, Metric: "cpu", Tint: "warn", TS: clock.Now().Add(-time.Hour)}},
			seen:  []clickhouse.NodeSeen{{OrgID: org, NodeID: id, TS: clock.Now().Add(-5 * time.Second)}},
		},
		marks: []clickhouse.AlertMark{{OrgID: org, AlertID: alert, AckedBy: &by}},
	}
	n := newCountingNotifier()
	node := notifyNode(id)
	e := New(nil, clock, nil, st, newFakeConfig(store.DefaultServerSettings()), n)
	if err := e.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Evaluate(context.Background(), org, id, &node, 0.95, 0.10, 0.10)
	if got := n.wait(0); len(got) != 0 {
		t.Fatalf("an acked alert must stay quiet after a restart: %+v", got)
	}
}

func TestAlerter_IgnoreClosesQuietly(t *testing.T) {
	clock := newClock()
	sink := &capturingSink{}
	st := &recordingStore{}
	n := newCountingNotifier()
	org, id := uuid.New(), uuid.New()
	node := notifyNode(id)
	e := New(nil, clock, sink, st, newFakeConfig(store.DefaultServerSettings()), n)
	high := func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(85)} }

	runProcs(e, clock, org, id, &node, 0, 32, high)
	if got := n.wait(1); len(got) != 1 {
		t.Fatalf("open: %+v", got)
	}
	node.IgnoredProcs = []string{"OrbStack Helper"}
	runProcs(e, clock, org, id, &node, 33, 33, high)

	ev := sink.snapshot()
	if len(ev) != 2 || ev[1].Tint != string(TintOK) || ev[1].Notify || ev[1].AlertID != ev[0].AlertID {
		t.Fatalf("events = %+v", ev)
	}
	if got := n.wait(0); len(got) != 0 {
		t.Fatalf("an ignored process must close with no notification: %+v", got)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if p := st.events[1].Payload; !strings.Contains(p, `"closed":"ignored"`) {
		t.Errorf("payload = %s", p)
	}
}

func TestAlerter_MuteClosesQuietly(t *testing.T) {
	for _, scope := range []string{"host", "org"} {
		t.Run(scope, func(t *testing.T) {
			v := newAckEnv(t)
			v.cpu(0.10)
			v.cpu(0.95)
			if got := v.n.wait(1); len(got) != 1 {
				t.Fatalf("open: %+v", got)
			}
			if scope == "host" {
				v.node.MutedSignals = store.MutedSignals{store.MetricCPU}
			} else {
				s := store.DefaultServerSettings()
				s.NotifyDebounceSeconds = 0
				s.Muted = store.MutedSignals{store.MetricCPU}
				v.cfg.mu.Lock()
				v.cfg.settings[v.org] = s
				v.cfg.mu.Unlock()
				v.e.InvalidateSettings(v.org)
			}
			v.cpu(0.95)
			ev := v.sink.snapshot()
			if len(ev) != 2 || ev[1].Tint != string(TintOK) || ev[1].Notify || ev[1].AlertID != ev[0].AlertID {
				t.Fatalf("events = %+v", ev)
			}
			if got := v.n.wait(0); len(got) != 0 {
				t.Fatalf("a muted alert must close with no notification: %+v", got)
			}
			v.st.mu.Lock()
			payload := v.st.events[1].Payload
			v.st.mu.Unlock()
			if !strings.Contains(payload, `"reason":"muted"`) {
				t.Errorf("payload = %s", payload)
			}
			// A muted metric stays quiet.
			v.cpu(0.99)
			if got := v.n.wait(0); len(got) != 0 {
				t.Fatalf("muted metric notified: %+v", got)
			}
		})
	}
}
