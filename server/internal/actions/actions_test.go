package actions

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

type fakeAlerts struct {
	rows map[uuid.UUID]clickhouse.AlertRow
}

func (fakeAlerts) Ready() bool { return true }

func (f fakeAlerts) Alert(_ context.Context, _, id uuid.UUID) (clickhouse.AlertRow, error) {
	if r, ok := f.rows[id]; ok {
		return r, nil
	}
	return clickhouse.AlertRow{}, clickhouse.ErrNoAlert
}

func (f fakeAlerts) AlertsByID(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]clickhouse.AlertRow, error) {
	out := map[uuid.UUID]clickhouse.AlertRow{}
	for _, id := range ids {
		if r, ok := f.rows[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

func (fakeAlerts) AlertMarks(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]clickhouse.AlertMark, error) {
	return nil, nil
}

type fakeJournal struct {
	mu    sync.Mutex
	lines []notify.Line
}

func (j *fakeJournal) Write(_ context.Context, lines ...notify.Line) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lines = append(j.lines, lines...)
	return nil
}

type fakeEngine struct {
	acks        []notify.Ack
	invalidated int
}

func (e *fakeEngine) Acked(_ context.Context, a notify.Ack) { e.acks = append(e.acks, a) }
func (e *fakeEngine) InvalidateSettings(uuid.UUID)          { e.invalidated++ }

type fakeEvents struct{ types []string }

func (e *fakeEvents) BroadcastOrg(_ uuid.UUID, m wire.WSMessage) { e.types = append(e.types, m.Type) }

type env struct {
	svc     *Service
	st      store.ConfigStore
	journal *fakeJournal
	engine  *fakeEngine
	events  *fakeEvents
	actor   Actor
	node    uuid.UUID
	open    uuid.UUID
	closed  uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{st: st, journal: &fakeJournal{}, engine: &fakeEngine{}, events: &fakeEvents{},
		node: uuid.New(), open: uuid.New(), closed: uuid.New(),
		actor: Actor{OrgID: store.ZeroOrg, By: uuid.New()}}
	if err := st.PutNode(t.Context(), store.ZeroOrg, store.Node{ID: e.node, DisplayName: "web1", Kind: "ssh", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	row := func(id uuid.UUID, tint string) clickhouse.AlertRow {
		return clickhouse.AlertRow{EventID: id, AlertID: id, NodeID: e.node, Kind: "threshold_crossed", Metric: "cpu", Tint: tint, TS: time.Now(), Payload: `{"value":0.9}`}
	}
	alerts := fakeAlerts{rows: map[uuid.UUID]clickhouse.AlertRow{e.open: row(e.open, "warn"), e.closed: row(e.closed, "ok")}}
	e.svc = NewService(st, alerts, e.journal, e.engine, e.events, nil)
	return e
}

func TestNewActor(t *testing.T) {
	user := uuid.New()
	withUser := store.Token{ID: uuid.New(), OrgID: uuid.New(), UserID: &user}
	if got := NewActor(withUser, "", "").By; got != user {
		t.Errorf("token with a user: %s, want %s", got, user)
	}
	plain := store.Token{ID: uuid.New()}
	if got := NewActor(plain, "", "").By; got != plain.ID {
		t.Errorf("token with no user: %s, want %s", got, plain.ID)
	}
}

func TestServiceMarkAlert(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	a, err := e.svc.MarkAlert(ctx, e.actor, e.open, AlertAction{Kind: ActAck})
	if err != nil {
		t.Fatal(err)
	}
	if a.AckedBy == nil || *a.AckedBy != e.actor.By {
		t.Errorf("acked by = %v", a.AckedBy)
	}
	if len(e.journal.lines) != 1 || e.journal.lines[0].Kind != notify.LineAcked {
		t.Errorf("journal = %+v", e.journal.lines)
	}
	if len(e.engine.acks) != 1 || e.engine.acks[0].Undo {
		t.Errorf("engine acks = %+v", e.engine.acks)
	}
	if !slices.Equal(e.events.types, []string{"alert_updated"}) {
		t.Errorf("events = %v", e.events.types)
	}
	if _, err := e.svc.MarkAlert(ctx, e.actor, e.closed, AlertAction{Kind: ActAck}); !errors.Is(err, ErrNotFound) {
		t.Errorf("closed alert: %v, want ErrNotFound", err)
	}
	past := time.Now().Add(-time.Hour)
	if _, err := e.svc.MarkAlert(ctx, e.actor, e.open, AlertAction{Kind: ActSnooze, Until: &past}); !errors.Is(err, ErrBadUntil) {
		t.Errorf("past until: %v, want ErrBadUntil", err)
	}
	future := time.Now().Add(time.Hour)
	a, err = e.svc.MarkAlert(ctx, e.actor, e.open, AlertAction{Kind: ActSnooze, Until: &future})
	if err != nil || a.SnoozedUntil == nil {
		t.Errorf("snooze: %+v, %v", a, err)
	}
}

func TestServiceMarkAlertsBulk(t *testing.T) {
	e := newEnv(t)
	unknown := uuid.New()
	res, err := e.svc.MarkAlerts(t.Context(), e.actor, []uuid.UUID{e.open, e.open, unknown, e.closed}, AlertAction{Kind: ActAck}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("%d results, want 3", len(res))
	}
	if res[0].ID != e.open || res[0].Alert == nil || res[0].Err != nil {
		t.Errorf("open: %+v", res[0])
	}
	if !errors.Is(res[1].Err, ErrNotFound) || res[1].Alert != nil {
		t.Errorf("unknown: %+v", res[1])
	}
	if !errors.Is(res[2].Err, ErrNotOpen) {
		t.Errorf("closed: %+v", res[2])
	}
	if len(e.journal.lines) != 1 {
		t.Errorf("journal lines = %d, want 1", len(e.journal.lines))
	}
}

func TestServiceSetMute(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	res, err := e.svc.SetMute(ctx, e.actor, store.MetricCPU, &e.node, true)
	if err != nil || res.Node == nil || res.Settings != nil {
		t.Fatalf("host mute: %+v, %v", res, err)
	}
	if e.engine.invalidated != 0 {
		t.Errorf("a host mute invalidated the engine settings")
	}
	res, err = e.svc.SetMute(ctx, e.actor, store.MetricCPU, nil, true)
	if err != nil || res.Settings == nil || res.Node != nil {
		t.Fatalf("org mute: %+v, %v", res, err)
	}
	if e.engine.invalidated != 1 {
		t.Errorf("invalidated = %d, want 1", e.engine.invalidated)
	}
	if _, err := e.svc.SetMute(ctx, e.actor, "bogus", nil, true); !errors.Is(err, ErrBadMetric) {
		t.Errorf("bad metric: %v", err)
	}
}
