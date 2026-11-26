package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/internal/actions"
	"github.com/towertail/towertail/server/internal/assistant"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

type fakeWriterActions struct {
	diff      []store.FieldChange
	previews  int
	edits     int
	restores  []uuid.UUID
	modes     []actions.RestoreMode
	actors    []actions.Actor
	marks     []actions.AlertAction
	restoreEr error
	backup    uuid.UUID
}

func (f *fakeWriterActions) Preview(_ context.Context, a actions.Actor, _ actions.RulesEdit) (actions.RulesResult, error) {
	f.previews++
	f.actors = append(f.actors, a)
	return actions.RulesResult{Diff: f.diff}, nil
}

func (f *fakeWriterActions) EditRules(_ context.Context, a actions.Actor, _ actions.RulesEdit) (actions.RulesResult, error) {
	f.edits++
	f.actors = append(f.actors, a)
	return actions.RulesResult{Backup: store.RulesBackup{ID: f.backup}}, nil
}

func (f *fakeWriterActions) Restore(_ context.Context, a actions.Actor, id uuid.UUID, m actions.RestoreMode) (actions.RulesResult, error) {
	f.restores, f.modes = append(f.restores, id), append(f.modes, m)
	f.actors = append(f.actors, a)
	return actions.RulesResult{}, f.restoreEr
}

func (f *fakeWriterActions) MarkAlert(_ context.Context, a actions.Actor, _ uuid.UUID, act actions.AlertAction) (wire.Alert, error) {
	f.marks = append(f.marks, act)
	f.actors = append(f.actors, a)
	return wire.Alert{}, nil
}

func (f *fakeWriterActions) SnoozeHost(context.Context, actions.Actor, uuid.UUID, *time.Time) (store.Node, error) {
	return store.Node{}, nil
}

func (f *fakeWriterActions) SetMute(context.Context, actions.Actor, store.ThresholdMetric, *uuid.UUID, bool) (actions.MuteResult, error) {
	return actions.MuteResult{}, nil
}

func (f *fakeWriterActions) IgnoreAlert(context.Context, actions.Actor, uuid.UUID, actions.IgnoreScope, bool) (wire.Alert, actions.RulesResult, error) {
	return wire.Alert{}, actions.RulesResult{}, nil
}

func (f *fakeWriterActions) IgnoreProcesses(context.Context, actions.Actor, []actions.IgnoreItem, actions.IgnoreScope, bool) (actions.RulesResult, []error, error) {
	return actions.RulesResult{}, []error{nil}, nil
}

type fakeWriterTools struct {
	inputs []json.RawMessage
}

func (f *fakeWriterTools) RulesEditFor(_ context.Context, _ string, args json.RawMessage) (actions.RulesEdit, error) {
	f.inputs = append(f.inputs, args)
	return actions.RulesEdit{}, nil
}

func (f *fakeWriterTools) SnoozeUntil(string, string, bool) (*time.Time, error) { return nil, nil }

type fakeSuggester struct {
	reqs []query.SuggestRequest
	err  error
}

func (f *fakeSuggester) SuggestThresholds(_ context.Context, r query.SuggestRequest) (query.Suggestion, error) {
	f.reqs = append(f.reqs, r)
	if f.err != nil {
		return query.Suggestion{}, f.err
	}
	cur := store.ThresholdPair{Warn: 0.8, Critical: 0.95}
	out := query.Suggestion{
		From: r.From, To: r.To, Clamped: true,
		Candidates: []query.Candidate{{Pair: cur, Current: true, Count: countOf(214)}},
	}
	for _, p := range r.Candidates {
		out.Candidates = append(out.Candidates, query.Candidate{Pair: p, Count: countOf(31)})
	}
	return out, nil
}

type fakeHosts struct{ node store.Node }

func (f fakeHosts) ResolveHost(context.Context, uuid.UUID, string) (store.Node, error) {
	return f.node, nil
}

func countOf(n int) (c struct {
	Alerts, Critical, Notifications int
	WarnTime, CriticalTime          time.Duration
}) {
	c.Notifications = n
	return c
}

func newTestWriter() (*assistantWriter, *fakeWriterActions, *fakeWriterTools, *fakeSuggester) {
	fa, ft, fs := &fakeWriterActions{backup: uuid.New()}, &fakeWriterTools{}, &fakeSuggester{}
	w := newAssistantWriter(fa, ft, fs, fakeHosts{store.Node{ID: uuid.New(), DisplayName: "web-1"}})
	w.now = func() time.Time { return time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC) }
	return w, fa, ft, fs
}

var testActor = assistant.Actor{OrgID: uuid.New(), UserID: uuid.New(), Role: store.RoleAdmin}

func TestWriterPlanThresholds(t *testing.T) {
	w, fa, _, fs := newTestWriter()
	fa.diff = []store.FieldChange{
		{Target: "org", Path: "thresholds.cpuWarn", Before: json.RawMessage(`0.8`), After: json.RawMessage(`0.97`)},
		{Target: "org", Path: "alerts.cpu.notify", Before: json.RawMessage(`"all"`), After: json.RawMessage(`"critical"`)},
	}
	in := json.RawMessage(`{"metric":"cpu","warn":0.97,"critical":0.99}`)
	ch, err := w.Plan(context.Background(), testActor, "set_thresholds", in)
	if err != nil {
		t.Fatal(err)
	}
	if fa.previews != 1 || fa.edits != 0 {
		t.Fatalf("previews %d edits %d", fa.previews, fa.edits)
	}
	if ch.Kind != "rules" || ch.MinRole != store.RoleAdmin || !ch.Backup || len(ch.Rows) != 2 {
		t.Fatalf("change = %+v", ch)
	}
	want := assistant.ChangeRow{ID: "r1", Target: "Organization", Field: "thresholds.cpuWarn", Old: "0.8", New: "0.97", Checked: true}
	if ch.Rows[0] != want || ch.Rows[1].Old != "all" || ch.Rows[1].New != "critical" {
		t.Fatalf("rows = %+v", ch.Rows)
	}
	if ch.Impact == nil || ch.Impact.Before != 214 || ch.Impact.After != 31 || ch.Impact.Window != "30d" || ch.Impact.Note == "" {
		t.Fatalf("impact = %+v", ch.Impact)
	}
	if len(fs.reqs) != 1 || len(fs.reqs[0].Candidates) != 1 || fs.reqs[0].Metric != store.MetricCPU {
		t.Fatalf("suggest = %+v", fs.reqs)
	}
	if string(ch.Input) != string(in) {
		t.Fatalf("input = %s", ch.Input)
	}
	if a := fa.actors[0]; a.Client != store.ClientAI || a.Device != "assistant" || a.OrgID != testActor.OrgID || a.By != testActor.UserID {
		t.Fatalf("actor = %+v", a)
	}

	// A replay error gives no impact. The change stays.
	fs.err = errors.New("no history")
	if ch, err = w.Plan(context.Background(), testActor, "set_thresholds", in); err != nil || ch.Impact != nil || len(ch.Rows) != 2 {
		t.Fatalf("with error: %+v %v", ch, err)
	}

	// One value: the writer reads the current pair first.
	fs.err, fs.reqs = nil, nil
	ch, err = w.Plan(context.Background(), testActor, "set_thresholds", json.RawMessage(`{"metric":"cpu","warn":0.9}`))
	if err != nil || len(fs.reqs) != 2 || fs.reqs[1].Candidates[0] != (store.ThresholdPair{Warn: 0.9, Critical: 0.95}) || ch.Impact == nil {
		t.Fatalf("partial: %+v %v %+v", ch, err, fs.reqs)
	}
}

func TestWriterPlanAlertAction(t *testing.T) {
	w, fa, _, fs := newTestWriter()
	id := uuid.New()
	ch, err := w.Plan(context.Background(), testActor, "ack_alert", json.RawMessage(`{"alert_id":"`+id.String()+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if fa.previews != 0 || len(fs.reqs) != 0 {
		t.Fatal("an alert action must not preview or replay")
	}
	if ch.Kind != "alerts" || ch.MinRole != store.RoleMember || ch.Backup || ch.Impact != nil || len(ch.Rows) != 1 {
		t.Fatalf("change = %+v", ch)
	}
	r := ch.Rows[0]
	if r.Target != "alert "+id.String() || r.Field != "ack" || r.Old != "open" || r.New != "acknowledged" || !r.Checked {
		t.Fatalf("row = %+v", r)
	}
	if _, err := w.Plan(context.Background(), testActor, "ack_alert", json.RawMessage(`{"alert_id":"x"}`)); err == nil {
		t.Fatal("a bad alert id must fail")
	}
}

func TestWriterApplyThresholdsDropsUncheckedField(t *testing.T) {
	w, fa, ft, _ := newTestWriter()
	ch := assistant.Change{
		Tool: "set_thresholds", Kind: "rules", Input: json.RawMessage(`{"metric":"cpu","warn":0.9,"critical":0.99}`),
		Rows: []assistant.ChangeRow{
			{ID: "r1", Field: "thresholds.cpuCritical"},
			{ID: "r2", Field: "thresholds.cpuWarn"},
		},
	}
	ap, err := w.Apply(context.Background(), testActor, ch, []string{"r1"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(ft.inputs[0], &got); err != nil {
		t.Fatal(err)
	}
	if _, has := got["warn"]; has || got["critical"] != 0.99 || got["metric"] != "cpu" {
		t.Fatalf("input = %v", got)
	}
	if fa.edits != 1 || ap.BackupID != fa.backup {
		t.Fatalf("edits %d backup %v", fa.edits, ap.BackupID)
	}

	// Other tools cannot drop a row.
	ch.Tool, ch.Input = "ignore_process", json.RawMessage(`{"name":"x"}`)
	_, err = w.Apply(context.Background(), testActor, ch, []string{"r1"})
	var ce *codedError
	if !errors.As(err, &ce) || ce.Status != http.StatusBadRequest {
		t.Fatalf("split of ignore_process: %v", err)
	}
}

func TestWriterUndo(t *testing.T) {
	w, fa, _, _ := newTestWriter()
	backup := uuid.New()
	rules := assistant.Change{Tool: "set_thresholds", Kind: "rules"}
	if err := w.Undo(context.Background(), testActor, rules, assistant.Applied{BackupID: backup}); err != nil {
		t.Fatal(err)
	}
	if len(fa.restores) != 1 || fa.restores[0] != backup || fa.modes[0] != actions.RestoreIfUnchanged {
		t.Fatalf("restore = %v %v", fa.restores, fa.modes)
	}

	fa.restoreEr = &actions.ConflictError{Targets: []string{"org"}}
	err := w.Undo(context.Background(), testActor, rules, assistant.Applied{BackupID: backup})
	var he *echo.HTTPError
	if !errors.As(err, &he) || he.Code != http.StatusConflict || !strings.Contains(he.Message.(string), "org") {
		t.Fatalf("conflict = %v", err)
	}

	id := uuid.New()
	in := json.RawMessage(`{"alert_id":"` + id.String() + `"}`)
	ack := assistant.Change{Tool: "ack_alert", Kind: "alerts", Input: in}
	ap, err := w.Apply(context.Background(), testActor, ack, nil)
	if err != nil || len(ap.Inverse) == 0 {
		t.Fatalf("apply ack: %+v %v", ap, err)
	}
	if err := w.Undo(context.Background(), testActor, ack, ap); err != nil {
		t.Fatal(err)
	}
	if len(fa.marks) != 2 || fa.marks[0].Undo || !fa.marks[1].Undo || fa.marks[1].Kind != actions.ActAck {
		t.Fatalf("marks = %+v", fa.marks)
	}
	for _, a := range fa.actors {
		if a.Client != store.ClientAI || a.Device != "assistant" {
			t.Fatalf("actor = %+v", a)
		}
	}
	if slices.Contains(fa.restores, uuid.Nil) {
		t.Fatal("a nil backup must not restore")
	}
}
