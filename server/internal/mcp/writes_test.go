package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/actions"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

var writeToolNames = []string{"ack_alert", "snooze_alert", "snooze_host", "mute_metric", "set_thresholds", "ignore_process", "undo_rules_change"}

// fakeAlertReader holds alert rows for the actions service.
type fakeAlertReader map[uuid.UUID]clickhouse.AlertRow

func (fakeAlertReader) Ready() bool { return true }

func (f fakeAlertReader) Alert(_ context.Context, _, id uuid.UUID) (clickhouse.AlertRow, error) {
	if r, ok := f[id]; ok {
		return r, nil
	}
	return clickhouse.AlertRow{}, clickhouse.ErrNoAlert
}

func (f fakeAlertReader) AlertsByID(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]clickhouse.AlertRow, error) {
	out := map[uuid.UUID]clickhouse.AlertRow{}
	for _, id := range ids {
		if r, ok := f[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

func (fakeAlertReader) AlertMarks(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]clickhouse.AlertMark, error) {
	return nil, nil
}

type fakeJournal struct{}

func (fakeJournal) Write(context.Context, ...notify.Line) error { return nil }

type fakeEngine struct{}

func (fakeEngine) Acked(context.Context, notify.Ack) {}
func (fakeEngine) InvalidateSettings(uuid.UUID)      {}

type fakeEvents struct{}

func (fakeEvents) BroadcastOrg(uuid.UUID, wire.WSMessage) {}

// writeQueries reads the hosts and the rules from the store, so a tool sees
// its own writes.
type writeQueries struct {
	*fakeQueries
	st   store.ConfigStore
	rows fakeAlertReader
}

func (q writeQueries) AlertDetail(_ context.Context, _, id uuid.UUID) (wire.AlertDetail, error) {
	r, ok := q.rows[id]
	if !ok {
		return wire.AlertDetail{}, clickhouse.ErrNoAlert
	}
	return wire.AlertDetail{Alert: query.AlertFromRow(r, clickhouse.AlertMark{})}, nil
}

func (q writeQueries) ResolveHost(ctx context.Context, org uuid.UUID, ref string) (store.Node, error) {
	nodes, err := q.st.ListNodes(ctx, org)
	if err != nil {
		return store.Node{}, err
	}
	for _, n := range nodes {
		if n.DisplayName == ref || n.ID.String() == ref {
			return n, nil
		}
	}
	return store.Node{}, query.ErrHostNotFound
}

func (q writeQueries) Rules(ctx context.Context, org, node uuid.UUID) (query.RulesResult, error) {
	cfg, err := q.st.GetSettings(ctx, org)
	if err != nil {
		return query.RulesResult{}, err
	}
	res := query.RulesResult{NodeID: node, Org: cfg.Rules(), Cooldown: time.Duration(cfg.NotifyCooldownSeconds) * time.Second}
	if node != uuid.Nil {
		n, err := q.st.GetNode(ctx, org, node)
		if err != nil {
			return query.RulesResult{}, err
		}
		h := n.Rules()
		eff := res.Org.For(h)
		res.Host, res.Effective = &h, &eff
	}
	return res, nil
}

type writeEnv struct {
	srv     *Server
	st      store.ConfigStore
	svc     *actions.Service
	logs    *bytes.Buffer
	web, db store.Node
	open    uuid.UUID
	closed  uuid.UUID
	proc    uuid.UUID
	user    uuid.UUID
}

func newWriteEnv(t *testing.T, ai store.AIChanges) *writeEnv {
	t.Helper()
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := newFake()
	e := &writeEnv{st: st, logs: &bytes.Buffer{}, web: f.nodes[0], db: f.nodes[1], open: uuid.New(), closed: uuid.New(), proc: uuid.New(), user: uuid.New()}
	for _, n := range f.nodes {
		n.Kind, n.Enabled = "ssh", true
		if err := st.PutNode(t.Context(), orgA, n); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := st.GetSettings(t.Context(), orgA)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MCP.AIChanges = ai
	if err := st.PutSettings(t.Context(), orgA, cfg); err != nil {
		t.Fatal(err)
	}
	row := func(id uuid.UUID, kind, tint, payload string) clickhouse.AlertRow {
		return clickhouse.AlertRow{EventID: id, AlertID: id, NodeID: e.web.ID, Kind: kind, Metric: "cpu", Tint: tint, TS: now, Payload: payload}
	}
	rows := fakeAlertReader{
		e.open:   row(e.open, "threshold_crossed", "warn", `{"value":0.9}`),
		e.closed: row(e.closed, "threshold_crossed", "ok", `{"value":0.1}`),
		e.proc:   row(e.proc, "long_process", "warn", `{"value":90,"pid":42,"name":"node","cmd":"node server.js","cpu_pct":95}`),
	}
	e.svc = actions.NewService(st, rows, fakeJournal{}, fakeEngine{}, fakeEvents{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.srv = New(writeQueries{f, st, rows}, Options{Version: "test", Log: slog.New(slog.NewTextHandler(e.logs, nil)), Settings: st, Actions: e.svc})
	e.srv.now = time.Now
	return e
}

// token makes an oauth token of the user with the scopes.
func (e *writeEnv) token(scopes ...store.Scope) store.Token {
	return store.Token{ID: uuid.New(), OrgID: orgA, Kind: store.TokenKindOAuth, Label: "Claude", UserID: &e.user, Scopes: append([]store.Scope{store.ScopeFleetRead}, scopes...)}
}

var (
	alertsScope = []store.Scope{store.ScopeAlertsWrite}
	bothScopes  = []store.Scope{store.ScopeAlertsWrite, store.ScopeRulesWrite}
)

func ctxOf(tok store.Token, role store.Role) context.Context {
	return authctx.WithRole(authctx.WithToken(context.Background(), tok), role)
}

func (e *writeEnv) session(t *testing.T, tok store.Token, role store.Role) *sdk.ClientSession {
	t.Helper()
	serverT, clientT := sdk.NewInMemoryTransports()
	ss, err := e.srv.sets[ToolSetLatest].srv.Connect(ctxOf(tok, role), serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (e *writeEnv) member(t *testing.T) *sdk.ClientSession {
	return e.session(t, e.token(alertsScope...), store.RoleMember)
}

func (e *writeEnv) admin(t *testing.T) *sdk.ClientSession {
	return e.session(t, e.token(bothScopes...), store.RoleAdmin)
}

func (e *writeEnv) node(t *testing.T, id uuid.UUID) store.Node {
	t.Helper()
	n, err := e.st.GetNode(t.Context(), orgA, id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *writeEnv) settings(t *testing.T) store.ServerSettings {
	t.Helper()
	cfg, err := e.st.GetSettings(t.Context(), orgA)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// failure gives the text of a tool error.
func failure(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	if !res.IsError {
		t.Fatalf("no tool error, result: %s", text(res))
	}
	return text(res)
}

func fieldOf(ch Change, path string) *ChangeField {
	for i := range ch.Fields {
		if ch.Fields[i].Path == path {
			return &ch.Fields[i]
		}
	}
	return nil
}

func TestWriteToolsAnnotations(t *testing.T) {
	s := newServer(newFake())
	for _, set := range toolSets {
		tools := map[string]*sdk.Tool{}
		for _, tl := range s.Tools(set) {
			tools[tl.Name] = tl
		}
		for _, name := range writeToolNames {
			tl := tools[name]
			if tl == nil {
				t.Fatalf("%s: no tool %s", set, name)
			}
			a := tl.Annotations
			if tl.Title == "" || a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
				t.Errorf("%s/%s: hints = %+v", set, name, a)
			}
			if a.IdempotentHint == (name == "undo_rules_change") {
				t.Errorf("%s/%s: idempotent hint = %v", set, name, a.IdempotentHint)
			}
		}
	}
}

func TestAckAlertTool(t *testing.T) {
	e := newWriteEnv(t, store.AIChangesAlerts)
	out := decode[AlertChangeOut](t, call(t, e.member(t), "ack_alert", map[string]any{"alert_id": e.open.String()}))
	if out.Alert.State != "acked" || out.Alert.AckedBy != e.user.String() || out.Alert.Host != "web-1" {
		t.Fatalf("alert = %+v", out.Alert)
	}
	f := fieldOf(out.Change, "acked")
	if len(out.Change.Targets) != 1 || out.Change.Targets[0].Kind != "alert" || out.Change.Targets[0].ID != e.open.String() || f == nil || f.After != true || out.Change.BackupID != "" {
		t.Fatalf("change = %+v", out.Change)
	}
	undo := decode[AlertChangeOut](t, call(t, e.member(t), "ack_alert", map[string]any{"alert_id": e.open.String(), "undo": true}))
	if f := fieldOf(undo.Change, "acked"); f == nil || f.After != false || undo.Alert.State != "open" {
		t.Fatalf("undo = %+v", undo)
	}

	msg := failure(t, call(t, e.session(t, e.token(alertsScope...), store.RoleViewer), "ack_alert", map[string]any{"alert_id": e.open.String()}))
	if !strings.Contains(msg, "Your role is viewer") {
		t.Fatalf("viewer: %q", msg)
	}
	_, _, err := e.srv.ackAlert(ctxOf(e.token(alertsScope...), store.RoleViewer), nil, AckAlertIn{AlertID: e.open.String()})
	if !errors.Is(err, ErrRole) {
		t.Fatalf("viewer error = %v, want ErrRole", err)
	}
	for _, id := range []string{e.closed.String(), uuid.NewString()} {
		if msg := failure(t, call(t, e.member(t), "ack_alert", map[string]any{"alert_id": id})); msg != "alert not found" {
			t.Errorf("alert %s: %q", id, msg)
		}
	}
	if msg := failure(t, call(t, e.member(t), "ack_alert", map[string]any{"alert_id": "x"})); !strings.Contains(msg, "alert_id must be") {
		t.Errorf("bad id: %q", msg)
	}
}

func TestSnoozeToolsDuration(t *testing.T) {
	e := newWriteEnv(t, store.AIChangesAlerts)
	cs := e.member(t)
	near := func(got string, want time.Time) {
		t.Helper()
		g, err := time.Parse(time.RFC3339, got)
		if err != nil || g.Sub(want).Abs() > 5*time.Second {
			t.Fatalf("snoozed_until = %q, want about %s", got, want)
		}
	}
	out := decode[AlertChangeOut](t, call(t, cs, "snooze_alert", map[string]any{"alert_id": e.open.String(), "for": "2h"}))
	near(out.Alert.SnoozedUntil, time.Now().Add(2*time.Hour))
	if f := fieldOf(out.Change, "snoozed_until"); f == nil || f.After != out.Alert.SnoozedUntil {
		t.Fatalf("change = %+v", out.Change)
	}
	until := time.Now().Add(26 * time.Hour).UTC().Format(time.RFC3339)
	out = decode[AlertChangeOut](t, call(t, cs, "snooze_alert", map[string]any{"alert_id": e.open.String(), "until": until}))
	if out.Alert.SnoozedUntil != until {
		t.Fatalf("until = %q, want %q", out.Alert.SnoozedUntil, until)
	}
	undo := decode[AlertChangeOut](t, call(t, cs, "snooze_alert", map[string]any{"alert_id": e.open.String(), "undo": true}))
	if undo.Alert.SnoozedUntil != "" || fieldOf(undo.Change, "snoozed_until").After != nil {
		t.Fatalf("undo = %+v", undo)
	}

	host := decode[SnoozeHostOut](t, call(t, cs, "snooze_host", map[string]any{"host": "web-1", "for": "30m"}))
	near(host.SnoozedUntil, time.Now().Add(30*time.Minute))
	if host.Host != "web-1" || host.HostID != e.web.ID.String() || e.node(t, e.web.ID).SnoozedUntil == nil {
		t.Fatalf("host = %+v", host)
	}
	again := decode[SnoozeHostOut](t, call(t, cs, "snooze_host", map[string]any{"host": e.web.ID.String(), "for": "1h"}))
	if f := fieldOf(again.Change, "snoozed_until"); f == nil || f.Before != host.SnoozedUntil {
		t.Fatalf("change = %+v, want before %s", again.Change, host.SnoozedUntil)
	}
	cleared := decode[SnoozeHostOut](t, call(t, cs, "snooze_host", map[string]any{"host": "web-1", "undo": true}))
	if cleared.SnoozedUntil != "" || e.node(t, e.web.ID).SnoozedUntil != nil {
		t.Fatalf("cleared = %+v", cleared)
	}

	for name, args := range map[string]map[string]any{
		"40d":          {"alert_id": e.open.String(), "for": "40d"},
		"1m":           {"alert_id": e.open.String(), "for": "1m"},
		"both":         {"alert_id": e.open.String(), "for": "1h", "until": until},
		"neither":      {"alert_id": e.open.String()},
		"bad until":    {"alert_id": e.open.String(), "until": "tomorrow"},
		"undo and for": {"alert_id": e.open.String(), "for": "1h", "undo": true},
	} {
		if msg := failure(t, call(t, cs, "snooze_alert", args)); strings.Contains(msg, "the query failed") || msg == "" {
			t.Errorf("%s: %q, want an input error", name, msg)
		}
	}
	if msg := failure(t, call(t, cs, "snooze_alert", map[string]any{"alert_id": e.open.String(), "for": "40d"})); !strings.Contains(msg, "5m to 30d") {
		t.Errorf("40d: %q", msg)
	}
	if msg := failure(t, call(t, cs, "snooze_host", map[string]any{"host": "nope", "for": "1h"})); !strings.Contains(msg, "not found") {
		t.Errorf("unknown host: %q", msg)
	}
}

func TestMuteMetricRights(t *testing.T) {
	e := newWriteEnv(t, store.AIChangesAlertsAndRules)
	out := decode[MuteMetricOut](t, call(t, e.member(t), "mute_metric", map[string]any{"metric": "cpu", "host": "web-1"}))
	if out.Target != "web-1" || !slices.Equal(out.Muted, []string{"cpu"}) || out.Change.BackupID != "" || fieldOf(out.Change, "muted") == nil {
		t.Fatalf("host mute = %+v", out)
	}
	if !e.node(t, e.web.ID).MutedSignals.Has(store.MetricCPU) {
		t.Fatal("the host is not muted")
	}
	off := false
	out = decode[MuteMetricOut](t, call(t, e.member(t), "mute_metric", map[string]any{"metric": "cpu", "host": "web-1", "muted": off}))
	if len(out.Muted) != 0 || e.node(t, e.web.ID).MutedSignals.Has(store.MetricCPU) {
		t.Fatalf("unmute = %+v", out)
	}

	alertsOnly := e.session(t, e.token(alertsScope...), store.RoleAdmin)
	if msg := failure(t, call(t, alertsOnly, "mute_metric", map[string]any{"metric": "disk"})); !strings.Contains(msg, "does not have the scope rules:write") {
		t.Fatalf("org mute with alerts:write: %q", msg)
	}
	if msg := failure(t, call(t, e.member(t), "mute_metric", map[string]any{"metric": "disk"})); !strings.Contains(msg, "scope rules:write") {
		t.Fatalf("org mute as member: %q", msg)
	}
	org := decode[MuteMetricOut](t, call(t, e.admin(t), "mute_metric", map[string]any{"metric": "disk"}))
	if org.Target != "org" || !slices.Equal(org.Muted, []string{"disk"}) || org.Change.BackupID == "" || fieldOf(org.Change, "muted") == nil {
		t.Fatalf("org mute = %+v", org)
	}
	if msg := failure(t, call(t, e.admin(t), "mute_metric", map[string]any{"metric": "nope"})); !strings.Contains(msg, "metric must be one of") {
		t.Fatalf("bad metric: %q", msg)
	}
}

func TestSetThresholdsHost(t *testing.T) {
	e := newWriteEnv(t, store.AIChangesAlertsAndRules)
	cs := e.admin(t)
	out := decode[SetThresholdsOut](t, call(t, cs, "set_thresholds", map[string]any{"host": "web-1", "metric": "cpu", "warn": 0.85}))
	org := e.settings(t).Thresholds
	if out.Target != "web-1" || out.Effective.Warn == nil || *out.Effective.Warn != 0.85 || out.Effective.Critical == nil || *out.Effective.Critical != org.CPUCritical || !strings.HasPrefix(out.Effective.Unit, "ratio") {
		t.Fatalf("out = %+v", out)
	}
	ov := e.node(t, e.web.ID).ThresholdOverrides
	if ov == nil || ov.CPU == nil || ov.CPU.Warn != 0.85 {
		t.Fatalf("override = %+v", ov)
	}
	f := fieldOf(out.Change, "thresholds.cpu.warn")
	if len(out.Change.Targets) != 1 || out.Change.Targets[0].ID != e.web.ID.String() || f == nil || f.After != 0.85 || out.Change.BackupID == "" {
		t.Fatalf("change = %+v", out.Change)
	}
	if got := e.node(t, e.web.ID).RulesOrigin; got == nil || got.Client != store.ClientAI || got.Device != "Claude" {
		t.Fatalf("origin = %+v, want client ai and device Claude", got)
	}

	rule := decode[SetThresholdsOut](t, call(t, cs, "set_thresholds", map[string]any{"host": "web-1", "metric": "cpu", "sustain_seconds": 600}))
	if rule.Effective.SustainSeconds == nil || *rule.Effective.SustainSeconds != 600 || rule.Effective.Notify != string(store.NotifyCritical) || *rule.Effective.Warn != 0.85 {
		t.Fatalf("sustain = %+v", rule.Effective)
	}
	cleared := decode[SetThresholdsOut](t, call(t, cs, "set_thresholds", map[string]any{"host": "web-1", "metric": "cpu", "clear": true}))
	n := e.node(t, e.web.ID)
	if n.ThresholdOverrides != nil || n.AlertOverrides != nil || *cleared.Effective.Warn != org.CPUWarn {
		t.Fatalf("clear: node = %+v, effective = %+v", n, cleared.Effective)
	}

	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"percent":       {map[string]any{"host": "web-1", "metric": "cpu", "warn": 85}, "cpu limits are ratios from 0 to 1 (0.9 is 90 %)."},
		"warn>critical": {map[string]any{"host": "web-1", "metric": "cpu", "warn": 0.9, "critical": 0.8}, "warn must not be more than critical"},
		"partial":       {map[string]any{"host": "web-1", "metric": "cpu", "warn": 0.99}, "The current cpu limits are warn 0.99 and critical"},
		"nothing":       {map[string]any{"host": "web-1", "metric": "cpu"}, "set one of"},
		"no metric":     {map[string]any{"host": "web-1", "warn": 0.5}, "metric is required"},
		"long":          {map[string]any{"metric": "long", "warn": 1}, "long has no limits"},
		"clear org":     {map[string]any{"metric": "cpu", "clear": true}, "clear works on a host"},
		"clear+warn":    {map[string]any{"host": "web-1", "metric": "cpu", "clear": true, "warn": 0.5}, "clear cannot"},
		"sustain":       {map[string]any{"metric": "cpu", "sustain_seconds": 99999}, "sustain_seconds must be"},
		"notify":        {map[string]any{"metric": "cpu", "notify": "loud"}, "notify must be"},
		"empty":         {map[string]any{}, "set one of"},
		"health pair":   {map[string]any{"metric": "health", "warn": 1}, "health has no warn"},
		"unknown host":  {map[string]any{"host": "nope", "metric": "cpu", "warn": 0.5}, "not found"},
	} {
		if msg := failure(t, call(t, cs, "set_thresholds", tc.args)); !strings.Contains(msg, tc.want) {
			t.Errorf("%s: %q, want %q", name, msg, tc.want)
		}
	}
}

func TestSetThresholdsOrgCooldown(t *testing.T) {
	e := newWriteEnv(t, store.AIChangesAlertsAndRules)
	cs := e.admin(t)
	if got := e.settings(t).NotifyCooldownSeconds; got != 600 {
		t.Fatalf("cooldown = %d, want the default 600", got)
	}
	out := decode[SetThresholdsOut](t, call(t, cs, "set_thresholds", map[string]any{"cooldown_seconds": 1800}))
	if out.Target != "org" || out.Effective.CooldownSeconds == nil || *out.Effective.CooldownSeconds != 1800 || e.settings(t).NotifyCooldownSeconds != 1800 {
		t.Fatalf("out = %+v", out)
	}
	if f := fieldOf(out.Change, "notify_cooldown_seconds"); f == nil || f.Before != float64(600) || f.After != float64(1800) || out.Change.BackupID == "" {
		t.Fatalf("change = %+v", out.Change)
	}
	backup, err := e.st.GetRulesBackup(t.Context(), orgA, uuid.MustParse(out.Change.BackupID))
	if err != nil || backup.Org == nil || backup.Org.NotifyCooldownSeconds != 600 {
		t.Fatalf("backup = %+v, err = %v; want the old cooldown 600", backup, err)
	}
	if msg := failure(t, call(t, cs, "set_thresholds", map[string]any{"host": "web-1", "cooldown_seconds": 60})); !strings.Contains(msg, "cooldown_seconds is for the organization") {
		t.Fatalf("cooldown with host: %q", msg)
	}
	if msg := failure(t, call(t, cs, "set_thresholds", map[string]any{"cooldown_seconds": 90000})); !strings.Contains(msg, "cooldown_seconds must be from 0 to 86400") {
		t.Fatalf("90000: %q", msg)
	}

	org := decode[SetThresholdsOut](t, call(t, cs, "set_thresholds", map[string]any{"metric": "mem", "warn": 0.7, "critical": 0.9, "notify": "all"}))
	if th := e.settings(t).Thresholds; th.MemWarn != 0.7 || th.MemCritical != 0.9 || org.Effective.Notify != "all" {
		t.Fatalf("org = %+v, thresholds = %+v", org, th)
	}
	procs := decode[SetThresholdsOut](t, call(t, cs, "set_thresholds", map[string]any{"metric": "procs", "warn": 100, "critical": 200, "sustain_seconds": 30}))
	if th := e.settings(t).Thresholds; th.ProcsWarn != 100 || th.ProcsCritical != 200 || e.settings(t).Alerts.Health.SustainSeconds != 30 || procs.Effective.Unit == "" {
		t.Fatalf("procs = %+v", procs)
	}
}

func TestSetThresholdsPolicy(t *testing.T) {
	e := newWriteEnv(t, store.AIChangesAlerts)
	before := e.settings(t).Thresholds
	msg := failure(t, call(t, e.admin(t), "set_thresholds", map[string]any{"metric": "cpu", "warn": 0.5}))
	if msg != "The org setting Changes by AI allows alert actions only." {
		t.Fatalf("msg = %q", msg)
	}
	_, _, err := e.srv.setThresholds(ctxOf(e.token(bothScopes...), store.RoleAdmin), nil, SetThresholdsIn{Metric: "cpu", Warn: new(0.5)})
	if !errors.Is(err, ErrAIChangesOff) {
		t.Fatalf("err = %v, want ErrAIChangesOff", err)
	}
	if e.settings(t).Thresholds != before {
		t.Fatal("a refused call changed the limits")
	}
	cfg := e.settings(t)
	cfg.MCP.AIChanges = store.AIChangesOff
	if err := e.st.PutSettings(t.Context(), orgA, cfg); err != nil {
		t.Fatal(err)
	}
	if msg := failure(t, call(t, e.member(t), "ack_alert", map[string]any{"alert_id": e.open.String()})); msg != "The org setting Changes by AI is off." {
		t.Fatalf("changes off: %q", msg)
	}
}

func TestIgnoreProcessTool(t *testing.T) {
	e := newWriteEnv(t, store.AIChangesAlertsAndRules)
	cs := e.admin(t)
	out := decode[IgnoreProcessOut](t, call(t, cs, "ignore_process", map[string]any{"name": "backup.sh"}))
	if out.Scope != "all" || out.Name != "backup.sh" || !slices.Contains(e.settings(t).LongProc.Ignore, "backup.sh") {
		t.Fatalf("out = %+v", out)
	}
	if out.Change.BackupID == "" || out.Change.Targets[0].Kind != "org" || fieldOf(out.Change, "long_proc.ignore") == nil {
		t.Fatalf("change = %+v", out.Change)
	}
	host := decode[IgnoreProcessOut](t, call(t, cs, "ignore_process", map[string]any{"name": "cron-job", "host": "db-1"}))
	if host.Scope != "host" || !slices.Contains(e.node(t, e.db.ID).IgnoredProcs, "cron-job") || fieldOf(host.Change, "ignored_procs") == nil {
		t.Fatalf("host = %+v", host)
	}
	undo := decode[IgnoreProcessOut](t, call(t, cs, "ignore_process", map[string]any{"name": "cron-job", "host": "db-1", "undo": true}))
	if len(e.node(t, e.db.ID).IgnoredProcs) != 0 || undo.Change.BackupID == "" {
		t.Fatalf("undo = %+v", undo)
	}

	if msg := failure(t, call(t, cs, "ignore_process", map[string]any{"alert_id": e.open.String()})); msg != "only a long_process alert has a process to ignore" {
		t.Fatalf("cpu alert: %q", msg)
	}
	for name, args := range map[string]map[string]any{
		"both":           {"name": "x", "alert_id": e.proc.String()},
		"neither":        {},
		"alert and host": {"alert_id": e.proc.String(), "host": "web-1"},
	} {
		if msg := failure(t, call(t, cs, "ignore_process", args)); strings.Contains(msg, "the query failed") {
			t.Errorf("%s: %q", name, msg)
		}
	}

	al := decode[IgnoreProcessOut](t, call(t, cs, "ignore_process", map[string]any{"alert_id": e.proc.String()}))
	if al.Name != "node" || al.Scope != "host" || al.Alert == nil || al.Alert.Host != "web-1" || !slices.Contains(e.node(t, e.web.ID).IgnoredProcs, "node") {
		t.Fatalf("alert ignore = %+v", al)
	}
	if msg := failure(t, call(t, e.member(t), "ignore_process", map[string]any{"name": "x"})); !strings.Contains(msg, "scope rules:write") {
		t.Fatalf("member: %q", msg)
	}
}

func TestUndoRulesChange(t *testing.T) {
	e := newWriteEnv(t, store.AIChangesAlertsAndRules)
	cs := e.admin(t)
	orig := e.settings(t).Thresholds
	set := decode[SetThresholdsOut](t, call(t, cs, "set_thresholds", map[string]any{"metric": "cpu", "warn": 0.5, "critical": 0.6}))
	if e.settings(t).Thresholds.CPUWarn != 0.5 {
		t.Fatal("the limit did not change")
	}
	undo := decode[UndoRulesChangeOut](t, call(t, cs, "undo_rules_change", map[string]any{"backup_id": set.Change.BackupID}))
	if got := e.settings(t).Thresholds; got != orig {
		t.Fatalf("thresholds = %+v, want %+v", got, orig)
	}
	if undo.RestoredBackupID != set.Change.BackupID || undo.Change.BackupID == "" || undo.Change.BackupID == set.Change.BackupID || fieldOf(undo.Change, "thresholds.cpu_warn") == nil {
		t.Fatalf("undo = %+v", undo)
	}
	if len(undo.Change.Targets) != 1 || undo.Change.Targets[0].Kind != "org" {
		t.Fatalf("targets = %+v", undo.Change.Targets)
	}

	msg := failure(t, call(t, cs, "undo_rules_change", map[string]any{"backup_id": set.Change.BackupID}))
	if want := "The rules of org changed after backup " + set.Change.BackupID + ". Nothing was restored."; msg != want {
		t.Fatalf("second undo: %q, want %q", msg, want)
	}
	if got := e.settings(t).Thresholds; got != orig {
		t.Fatal("a refused undo changed the limits")
	}
	if msg := failure(t, call(t, cs, "undo_rules_change", map[string]any{"backup_id": uuid.NewString()})); msg != "rules backup not found" {
		t.Fatalf("unknown backup: %q", msg)
	}
	if msg := failure(t, call(t, cs, "undo_rules_change", map[string]any{"backup_id": "x"})); !strings.Contains(msg, "backup_id must be") {
		t.Fatalf("bad id: %q", msg)
	}
	member := e.session(t, e.token(bothScopes...), store.RoleMember)
	if msg := failure(t, call(t, member, "undo_rules_change", map[string]any{"backup_id": set.Change.BackupID})); !strings.Contains(msg, "Your role is member") {
		t.Fatalf("member: %q", msg)
	}

	host := decode[SetThresholdsOut](t, call(t, cs, "set_thresholds", map[string]any{"host": "web-1", "metric": "mem", "warn": 0.6}))
	e.node(t, e.web.ID)
	decode[SetThresholdsOut](t, call(t, cs, "set_thresholds", map[string]any{"host": "web-1", "metric": "mem", "warn": 0.65}))
	msg = failure(t, call(t, cs, "undo_rules_change", map[string]any{"backup_id": host.Change.BackupID}))
	if !strings.Contains(msg, "The rules of web-1 changed after backup "+host.Change.BackupID) {
		t.Fatalf("host conflict: %q", msg)
	}
}

func TestWriteChangeOutput(t *testing.T) {
	e := newWriteEnv(t, store.AIChangesAlertsAndRules)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"ack_alert", map[string]any{"alert_id": e.open.String()}},
		{"snooze_alert", map[string]any{"alert_id": e.open.String(), "for": "1h"}},
		{"snooze_host", map[string]any{"host": "web-1", "for": "1h"}},
		{"mute_metric", map[string]any{"metric": "cpu", "host": "web-1"}},
		{"mute_metric", map[string]any{"metric": "zombies"}},
		{"set_thresholds", map[string]any{"host": "db-1", "metric": "disk", "critical": 0.97}},
		{"ignore_process", map[string]any{"name": "x"}},
	} {
		cs := e.admin(t)
		res := call(t, cs, tc.tool, tc.args)
		if res.IsError {
			t.Fatalf("%s: %s", tc.tool, text(res))
		}
		structured, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var a, b map[string]any
		if err := json.Unmarshal([]byte(text(res)), &a); err != nil {
			t.Fatalf("%s: the text is not JSON: %v", tc.tool, err)
		}
		if err := json.Unmarshal(structured, &b); err != nil {
			t.Fatal(err)
		}
		change, ok := b["change"].(map[string]any)
		if !ok || !reflect.DeepEqual(a, b) {
			t.Fatalf("%s: text %s and structured content %s differ or have no change", tc.tool, text(res), structured)
		}
		if _, ok := change["targets"].([]any); !ok {
			t.Errorf("%s: change has no targets: %v", tc.tool, change)
		}
		if _, ok := change["fields"].([]any); !ok {
			t.Errorf("%s: change has no fields: %v", tc.tool, change)
		}
	}
	set := e.settings(t)
	if o := set.RulesOrigin; o == nil || o.Client != store.ClientAI || o.Device != "Claude" || o.UserID != e.user {
		t.Fatalf("org origin = %+v", o)
	}
	if o := e.node(t, e.db.ID).RulesOrigin; o == nil || o.Client != store.ClientAI || o.Device != "Claude" {
		t.Fatalf("host origin = %+v", o)
	}
}

func TestWriteToolsNeedActions(t *testing.T) {
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := newFake()
	user := uuid.New()
	tok := store.Token{ID: uuid.New(), OrgID: orgA, Kind: store.TokenKindOAuth, UserID: &user, Scopes: []store.Scope{store.ScopeAlertsWrite}}
	s := New(f, Options{Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Settings: st})
	if got := len(s.Tools(ToolSetLatest)); got != 25 {
		t.Fatalf("%d tools, want 25", got)
	}
	_, _, err = s.ackAlert(ctxOf(tok, store.RoleAdmin), nil, AckAlertIn{AlertID: uuid.NewString()})
	if err == nil || err.Error() != "Changes are not available on this server." {
		t.Fatalf("err = %v", err)
	}
	_, _, err = s.ackAlert(ctxOf(store.Token{OrgID: orgA, Kind: store.TokenKindAPI}, store.RoleAdmin), nil, AckAlertIn{AlertID: uuid.NewString()})
	if !errors.Is(err, ErrNoScope) {
		t.Fatalf("api key: err = %v, want ErrNoScope", err)
	}
}

func TestWhoamiCan(t *testing.T) {
	e := newWriteEnv(t, store.AIChangesAlertsAndRules)
	cs := e.session(t, e.token(alertsScope...), store.RoleAdmin)
	out := decode[WhoamiOut](t, call(t, cs, "whoami", nil))
	if out.Can != (Can{Alerts: true, Rules: false}) || out.ChangesByAI != "alerts_and_rules" || !slices.Equal(out.Scopes, []string{"fleet:read", "alerts:write"}) {
		t.Fatalf("out = %+v", out)
	}
	all := decode[WhoamiOut](t, call(t, e.admin(t), "whoami", nil))
	if all.Can != (Can{Alerts: true, Rules: true}) || len(all.Scopes) != 3 {
		t.Fatalf("all = %+v", all)
	}
	viewer := decode[WhoamiOut](t, call(t, e.session(t, e.token(bothScopes...), store.RoleViewer), "whoami", nil))
	if viewer.Can != (Can{}) {
		t.Fatalf("viewer = %+v", viewer)
	}
	key := decode[WhoamiOut](t, call(t, e.session(t, store.Token{ID: uuid.New(), OrgID: orgA, Kind: store.TokenKindAPI}, store.RoleAdmin), "whoami", nil))
	if key.Can != (Can{}) || !slices.Equal(key.Scopes, []string{"fleet:read"}) || key.ChangesByAI != "alerts_and_rules" {
		t.Fatalf("api key = %+v", key)
	}
}

func TestRulesEditForMatchesApply(t *testing.T) {
	e := newWriteEnv(t, store.AIChangesAlertsAndRules)
	cs := e.admin(t)
	ctx := ctxOf(e.token(bothScopes...), store.RoleAdmin)
	actor := actions.NewActor(e.token(bothScopes...), store.ClientAI, "Claude")

	// check previews the edit of RulesEditFor, runs the tool and compares.
	check := func(tool string, args map[string]any) Change {
		t.Helper()
		raw, _ := json.Marshal(args)
		edit, err := e.srv.RulesEditFor(ctx, tool, raw)
		if err != nil {
			t.Fatalf("%s: RulesEditFor: %v", tool, err)
		}
		prev, err := e.svc.Preview(ctx, actor, edit)
		if err != nil {
			t.Fatalf("%s: Preview: %v", tool, err)
		}
		res := call(t, cs, tool, args)
		if res.IsError {
			t.Fatalf("%s: %s", tool, text(res))
		}
		var out struct {
			Change Change `json:"change"`
		}
		if err := json.Unmarshal([]byte(text(res)), &out); err != nil {
			t.Fatal(err)
		}
		got := rulesChange(actions.RulesResult{Diff: prev.Diff}).Fields
		if len(got) == 0 {
			t.Fatalf("%s: the preview has no change", tool)
		}
		if !reflect.DeepEqual(got, out.Change.Fields) {
			t.Fatalf("%s: preview fields %+v differ from the applied fields %+v", tool, got, out.Change.Fields)
		}
		return out.Change
	}
	check("set_thresholds", map[string]any{"metric": "cpu", "warn": 0.6, "critical": 0.7})
	check("set_thresholds", map[string]any{"host": "web-1", "metric": "mem", "warn": 0.55})
	check("set_thresholds", map[string]any{"host": "web-1", "metric": "mem", "sustain_seconds": 45})
	check("mute_metric", map[string]any{"metric": "ioPressure"})
	check("ignore_process", map[string]any{"name": "tool-a"})
	check("ignore_process", map[string]any{"name": "tool-b", "host": "db-1"})
	set := check("set_thresholds", map[string]any{"metric": "disk", "warn": 0.6})
	check("undo_rules_change", map[string]any{"backup_id": set.BackupID})

	for _, tc := range []struct {
		tool string
		args string
		want error
	}{
		{"ack_alert", `{}`, ErrUnknownTool},
		{"nope", `{}`, ErrUnknownTool},
	} {
		if _, err := e.srv.RulesEditFor(ctx, tc.tool, json.RawMessage(tc.args)); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.tool, err, tc.want)
		}
	}
	for _, tc := range []struct{ tool, args string }{
		{"mute_metric", `{"metric":"cpu","host":"web-1"}`},
		{"set_thresholds", `{"metric":"cpu"}`},
		{"set_thresholds", `[`},
		{"undo_rules_change", `{"backup_id":"x"}`},
	} {
		var in *inputError
		if _, err := e.srv.RulesEditFor(ctx, tc.tool, json.RawMessage(tc.args)); !errors.As(err, &in) {
			t.Errorf("%s %s: err = %v, want an input error", tc.tool, tc.args, err)
		}
	}
	if _, err := e.srv.RulesEditFor(context.Background(), "set_thresholds", nil); err == nil {
		t.Error("no token: want an error")
	}
}

func TestWriteAuditLog(t *testing.T) {
	e := newWriteEnv(t, store.AIChangesAlerts)
	decode[AlertChangeOut](t, call(t, e.member(t), "ack_alert", map[string]any{"alert_id": e.open.String()}))
	failure(t, call(t, e.admin(t), "set_thresholds", map[string]any{"metric": "cpu", "warn": 0.5}))
	failure(t, call(t, e.session(t, store.Token{ID: uuid.New(), OrgID: orgA, Kind: store.TokenKindAPI}, store.RoleOwner), "ack_alert", map[string]any{"alert_id": e.open.String()}))
	lines := strings.Split(strings.TrimSpace(e.logs.String()), "\n")
	var audit []string
	for _, l := range lines {
		if strings.Contains(l, "msg=audit") {
			audit = append(audit, l)
		}
	}
	if len(audit) != 3 {
		t.Fatalf("%d audit lines, want 3:\n%s", len(audit), e.logs)
	}
	for i, want := range []string{"tool=ack_alert", "tool=set_thresholds", "tool=ack_alert"} {
		if !strings.Contains(audit[i], want) {
			t.Errorf("line %d = %q, want %q", i, audit[i], want)
		}
	}
	for i, want := range []string{"result=ok", "result=denied", "result=denied"} {
		if !strings.Contains(audit[i], want) {
			t.Errorf("line %d = %q, want %q", i, audit[i], want)
		}
	}
	if !strings.Contains(audit[0], "client=Claude") || !strings.Contains(audit[0], "targets=[alert:"+e.open.String()+"]") {
		t.Errorf("ok line = %q", audit[0])
	}
}
