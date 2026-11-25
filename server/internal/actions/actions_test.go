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
	if list, _ := e.st.ListRulesBackups(ctx, store.ZeroOrg); len(list) != 0 {
		t.Errorf("a host mute made %d backups", len(list))
	}
	res, err = e.svc.SetMute(ctx, e.actor, store.MetricCPU, nil, true)
	if err != nil || res.Settings == nil || res.Node != nil {
		t.Fatalf("org mute: %+v, %v", res, err)
	}
	if e.engine.invalidated != 1 {
		t.Errorf("invalidated = %d, want 1", e.engine.invalidated)
	}
	if res.Rules == nil || res.Rules.Backup.ID == uuid.Nil || res.Rules.Backup.Org == nil || res.Rules.Backup.Org.Muted.Has(store.MetricCPU) {
		t.Errorf("org mute backup: %+v", res.Rules)
	}
	if len(res.Rules.Diff) != 1 || res.Rules.Diff[0].Target != "org" || res.Rules.Diff[0].Path != "muted" {
		t.Errorf("org mute diff: %+v", res.Rules.Diff)
	}
	if list, _ := e.st.ListRulesBackups(ctx, store.ZeroOrg); len(list) != 1 {
		t.Errorf("an org mute made %d backups, want 1", len(list))
	}
	// The same mute again changes nothing: no backup.
	if res, err = e.svc.SetMute(ctx, e.actor, store.MetricCPU, nil, true); err != nil || res.Rules.Backup.ID != uuid.Nil {
		t.Errorf("same mute: %+v, %v", res.Rules, err)
	}
	if _, err := e.svc.SetMute(ctx, e.actor, "bogus", nil, true); !errors.Is(err, ErrBadMetric) {
		t.Errorf("bad metric: %v", err)
	}
}

// countingStore counts the rules edits.
type countingStore struct {
	store.ConfigStore
	ruleEdits int
}

func (c *countingStore) EditRules(ctx context.Context, org uuid.UUID, ed store.RulesEdit) (store.RulesBackup, error) {
	c.ruleEdits++
	return c.ConfigStore.EditRules(ctx, org, ed)
}

// tick makes the clock of the service move one second for each call, so
// two changes have two origin times.
func (e *env) tick() {
	at := time.Now().UTC()
	e.svc.now = func() time.Time {
		at = at.Add(time.Second)
		return at
	}
}

func (e *env) addNode(t *testing.T, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := e.st.PutNode(t.Context(), store.ZeroOrg, store.Node{ID: id, DisplayName: name, Kind: "ssh", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) settings(t *testing.T) store.ServerSettings {
	t.Helper()
	cfg, err := e.st.GetSettings(t.Context(), store.ZeroOrg)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func cpuWarn(v float64) RulesEdit {
	return RulesEdit{Org: true, Apply: func(s *store.ServerSettings, _ map[uuid.UUID]*store.Node) error {
		s.Thresholds.CPUWarn = v
		return nil
	}}
}

func TestServiceEditRules(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	res, err := e.svc.EditRules(ctx, e.actor, RulesEdit{
		Org: true, Hosts: []uuid.UUID{e.node},
		Apply: func(s *store.ServerSettings, nodes map[uuid.UUID]*store.Node) error {
			s.NotifyCooldownSeconds = 900
			nodes[e.node].MutedSignals = store.MutedSignals{store.MetricDisk}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Backup.ID == uuid.Nil || res.Settings == nil || len(res.Nodes) != 1 {
		t.Fatalf("result: %+v", res)
	}
	if len(res.Diff) != 2 || res.Diff[0].Target != "org" || res.Diff[0].Path != "notifyCooldownSeconds" ||
		res.Diff[1].Target != e.node.String() || res.Diff[1].Path != "muted" {
		t.Errorf("diff: %+v", res.Diff)
	}
	if e.engine.invalidated != 1 || !slices.Equal(e.events.types, []string{"settings_updated", "node_updated"}) {
		t.Errorf("invalidated %d, events %v", e.engine.invalidated, e.events.types)
	}
	if o := e.settings(t).RulesOrigin; o == nil || o.UserID != e.actor.By || o.Client != store.ClientAPI {
		t.Errorf("origin: %+v", o)
	}
	res, err = e.svc.EditRules(ctx, e.actor, cpuWarn(0.75))
	if err != nil || res.Backup.ID != uuid.Nil || len(res.Diff) != 0 {
		t.Errorf("no change: %+v, %v", res, err)
	}
	boom := errors.New("boom")
	if _, err := e.svc.EditRules(ctx, e.actor, RulesEdit{Org: true, Apply: func(*store.ServerSettings, map[uuid.UUID]*store.Node) error { return boom }}); !errors.Is(err, boom) {
		t.Errorf("apply error: %v", err)
	}
}

func TestServiceIgnoreProcessesOneBackup(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	cs := &countingStore{ConfigStore: e.st}
	svc := NewService(cs, nil, e.journal, e.engine, e.events, nil)
	other := e.addNode(t, "web2")
	items := []IgnoreItem{{e.node, "node"}, {other, "python3"}, {e.node, "ruby"}}
	res, errs, err := svc.IgnoreProcesses(ctx, e.actor, items, ScopeHost, false)
	if err != nil || slices.ContainsFunc(errs, func(e error) bool { return e != nil }) {
		t.Fatalf("ignore: %v, %v", errs, err)
	}
	if cs.ruleEdits != 1 {
		t.Errorf("rules edits = %d, want 1", cs.ruleEdits)
	}
	list, _ := e.st.ListRulesBackups(ctx, store.ZeroOrg)
	if len(list) != 1 || len(list[0].Hosts) != 2 || res.Backup.ID != list[0].ID {
		t.Fatalf("backups: %+v, result %+v", list, res.Backup)
	}
	if n, _ := e.st.GetNode(ctx, store.ZeroOrg, e.node); !slices.Equal(n.IgnoredProcs, []string{"node", "ruby"}) {
		t.Errorf("ignored: %v", n.IgnoredProcs)
	}
	if len(res.Diff) != 2 {
		t.Errorf("diff: %+v", res.Diff)
	}

	// An unknown host fails alone.
	_, errs, err = svc.IgnoreProcesses(ctx, e.actor, []IgnoreItem{{uuid.New(), "x"}, {e.node, "go"}}, ScopeHost, false)
	if err != nil || !errors.Is(errs[0], store.ErrNotFound) || errs[1] != nil {
		t.Errorf("unknown host: %v, %v", errs, err)
	}
	// All hosts: one edit of the org rules, with one backup.
	cs.ruleEdits = 0
	res, errs, err = svc.IgnoreProcesses(ctx, e.actor, []IgnoreItem{{e.node, "a"}, {other, "b"}}, ScopeAll, false)
	if err != nil || errs[0] != nil || errs[1] != nil || cs.ruleEdits != 1 || res.Settings == nil || res.Backup.Org == nil {
		t.Fatalf("scope all: %+v %v %v, edits %d", res, errs, err, cs.ruleEdits)
	}
	if got := e.settings(t).LongProc.Ignore; !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("org ignore: %v", got)
	}
	// An undo removes the names. A second undo changes nothing: no backup.
	res, _, _ = svc.IgnoreProcesses(ctx, e.actor, []IgnoreItem{{e.node, "a"}}, ScopeAll, true)
	if res.Backup.ID == uuid.Nil || !slices.Equal(e.settings(t).LongProc.Ignore, []string{"b"}) {
		t.Errorf("undo: %+v", res)
	}
	if res, _, _ = svc.IgnoreProcesses(ctx, e.actor, []IgnoreItem{{e.node, "a"}}, ScopeAll, true); res.Backup.ID != uuid.Nil {
		t.Errorf("second undo made a backup")
	}
	if _, _, err := svc.IgnoreProcesses(ctx, e.actor, items, "x", false); !errors.Is(err, ErrBadInput) {
		t.Errorf("bad scope: %v", err)
	}
}

func TestServiceRestoreConflict(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	e.tick()
	setCPU := func(v float64) RulesEdit {
		return RulesEdit{Hosts: []uuid.UUID{e.node}, Apply: func(_ *store.ServerSettings, nodes map[uuid.UUID]*store.Node) error {
			nodes[e.node].ThresholdOverrides = &store.ThresholdOverrides{CPU: &store.ThresholdPair{Warn: v, Critical: 0.95}}
			return nil
		}}
	}
	first, err := e.svc.EditRules(ctx, e.actor, setCPU(0.8))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.EditRules(ctx, e.actor, setCPU(0.85)); err != nil {
		t.Fatal(err)
	}
	e.events.types = nil
	_, err = e.svc.Restore(ctx, e.actor, first.Backup.ID, RestoreIfUnchanged)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !slices.Equal(conflict.Targets, []string{"web1"}) {
		t.Fatalf("restore: %v, want a conflict with web1", err)
	}
	if n, _ := e.st.GetNode(ctx, store.ZeroOrg, e.node); n.ThresholdOverrides.CPU.Warn != 0.85 {
		t.Errorf("a conflict wrote: %+v", n.ThresholdOverrides)
	}
	if len(e.events.types) != 0 {
		t.Errorf("a conflict sent events: %v", e.events.types)
	}
	if list, _ := e.st.ListRulesBackups(ctx, store.ZeroOrg); len(list) != 2 {
		t.Errorf("%d backups, want 2", len(list))
	}
	res, err := e.svc.Restore(ctx, e.actor, first.Backup.ID, RestoreAll)
	if err != nil || res.Backup.ID == uuid.Nil {
		t.Fatalf("restore all: %+v, %v", res, err)
	}
	if n, _ := e.st.GetNode(ctx, store.ZeroOrg, e.node); n.ThresholdOverrides != nil {
		t.Errorf("restore all: %+v", n.ThresholdOverrides)
	}

	// With no change after the backup, the undo works, and it makes a backup.
	second, err := e.svc.EditRules(ctx, e.actor, setCPU(0.7))
	if err != nil {
		t.Fatal(err)
	}
	res, err = e.svc.Restore(ctx, e.actor, second.Backup.ID, RestoreIfUnchanged)
	if err != nil || res.Backup.ID == uuid.Nil || res.Backup.ID == second.Backup.ID {
		t.Fatalf("undo: %+v, %v", res, err)
	}
	if n, _ := e.st.GetNode(ctx, store.ZeroOrg, e.node); n.ThresholdOverrides != nil {
		t.Errorf("undo: %+v", n.ThresholdOverrides)
	}
	// The second undo of the same backup is a conflict.
	if _, err := e.svc.Restore(ctx, e.actor, second.Backup.ID, RestoreIfUnchanged); !errors.As(err, &conflict) {
		t.Errorf("second undo: %v, want a conflict", err)
	}

	// An org target and a deleted host.
	org, err := e.svc.EditRules(ctx, e.actor, cpuWarn(0.8))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.EditRules(ctx, e.actor, RulesEdit{Org: true, Hosts: []uuid.UUID{e.node}, Apply: func(s *store.ServerSettings, nodes map[uuid.UUID]*store.Node) error {
		s.Thresholds.CPUWarn = 0.82
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Restore(ctx, e.actor, org.Backup.ID, RestoreIfUnchanged); !errors.As(err, &conflict) || !slices.Equal(conflict.Targets, []string{"org"}) {
		t.Errorf("org conflict: %v", err)
	}
	if _, err := e.svc.Restore(ctx, e.actor, uuid.New(), RestoreIfUnchanged); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown backup: %v", err)
	}
}

func TestServicePreview(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	before := e.settings(t)
	res, err := e.svc.Preview(ctx, e.actor, cpuWarn(0.8))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Diff) != 1 || res.Diff[0].Target != "org" || res.Diff[0].Path != "thresholds.cpuWarn" ||
		string(res.Diff[0].Before) != "0.75" || string(res.Diff[0].After) != "0.8" {
		t.Errorf("diff: %+v", res.Diff)
	}
	if res.Backup.ID != uuid.Nil {
		t.Errorf("backup: %+v", res.Backup)
	}
	if list, _ := e.st.ListRulesBackups(ctx, store.ZeroOrg); len(list) != 0 {
		t.Errorf("%d backups", len(list))
	}
	if after := e.settings(t); after.Thresholds.CPUWarn != before.Thresholds.CPUWarn || after.RulesOrigin != nil {
		t.Errorf("settings changed: %+v", after.Thresholds)
	}
	if e.engine.invalidated != 0 || len(e.events.types) != 0 {
		t.Errorf("invalidated %d, events %v", e.engine.invalidated, e.events.types)
	}

	// An edit through a pointer of the stored value does not reach the store.
	if _, err := e.svc.EditRules(ctx, e.actor, RulesEdit{Hosts: []uuid.UUID{e.node}, Apply: func(_ *store.ServerSettings, nodes map[uuid.UUID]*store.Node) error {
		nodes[e.node].ThresholdOverrides = &store.ThresholdOverrides{Mem: &store.ThresholdPair{Warn: 0.8, Critical: 0.9}}
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	res, err = e.svc.Preview(ctx, e.actor, RulesEdit{Hosts: []uuid.UUID{e.node}, Apply: func(_ *store.ServerSettings, nodes map[uuid.UUID]*store.Node) error {
		nodes[e.node].ThresholdOverrides.Mem.Warn = 0.85
		return nil
	}})
	if err != nil || len(res.Diff) != 1 || res.Diff[0].Target != e.node.String() || res.Diff[0].Path != "thresholds.mem.warn" {
		t.Fatalf("host preview: %+v, %v", res.Diff, err)
	}
	if n, _ := e.st.GetNode(ctx, store.ZeroOrg, e.node); n.ThresholdOverrides.Mem.Warn != 0.8 {
		t.Errorf("a preview wrote: %+v", n.ThresholdOverrides.Mem)
	}

	if _, err := e.svc.Preview(ctx, e.actor, cpuWarn(0.95)); !errors.Is(err, store.ErrInvalidRules) {
		t.Errorf("warn over critical: %v, want ErrInvalidRules", err)
	}
	if _, err := e.svc.Preview(ctx, e.actor, RulesEdit{Hosts: []uuid.UUID{uuid.New()}, Apply: cpuWarn(0.8).Apply}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown host: %v, want ErrNotFound", err)
	}
	boom := errors.New("boom")
	if _, err := e.svc.Preview(ctx, e.actor, RulesEdit{Org: true, Apply: func(*store.ServerSettings, map[uuid.UUID]*store.Node) error { return boom }}); !errors.Is(err, boom) {
		t.Errorf("apply error: %v", err)
	}
}
