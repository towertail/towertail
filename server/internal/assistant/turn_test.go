package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/mcp"
	"github.com/towertail/towertail/server/store"
)

// round is one scripted model answer.
type round struct {
	parts []Part
	err   error
	// after runs after the first part is sent.
	after func()
}

type scripted struct {
	mu     sync.Mutex
	rounds []round
	reqs   []Request
}

func (m *scripted) Stream(_ context.Context, r Request) iter.Seq2[Part, error] {
	m.mu.Lock()
	m.reqs = append(m.reqs, r)
	i := len(m.reqs) - 1
	m.mu.Unlock()
	return func(yield func(Part, error) bool) {
		if i >= len(m.rounds) {
			yield(Part{}, errors.New("no script for this request"))
			return
		}
		rd := m.rounds[i]
		for j, p := range rd.parts {
			if !yield(p, nil) {
				return
			}
			if j == 0 && rd.after != nil {
				rd.after()
			}
		}
		if rd.err != nil {
			yield(Part{}, rd.err)
		}
	}
}

func txt(s string) Part { return Part{Kind: PartText, Text: s} }

func call(id, name, input string) Part {
	return Part{Kind: PartToolCall, Call: &ToolCall{ID: id, Name: name, Input: json.RawMessage(input)}}
}

func stop(r StopReason, in, out int) Part {
	return Part{Kind: PartStop, Stop: r, Usage: &Usage{Input: in, Output: out}}
}

var readSpecs = []mcp.ToolSpec{
	{Name: "alerts", Title: "List alerts", Description: "alerts", InputSchema: json.RawMessage(`{"type":"object"}`), ReadOnly: true},
	{Name: "host_metrics", Title: "Host metrics", Description: "metrics", InputSchema: json.RawMessage(`{"type":"object"}`), ReadOnly: true},
}

var writeNames = []string{"ack_alert", "snooze_alert", "snooze_host", "mute_metric", "set_thresholds", "ignore_process", "undo_rules_change"}

type fakeTools struct {
	mu     sync.Mutex
	out    map[string]string
	errs   map[string]error
	called []string
	args   map[string]json.RawMessage
	ctxs   []context.Context
}

func (f *fakeTools) ToolSpecs() []mcp.ToolSpec {
	specs := slices.Clone(readSpecs)
	for _, n := range writeNames {
		specs = append(specs, mcp.ToolSpec{Name: n, Title: n, Description: n, InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
	slices.SortFunc(specs, func(a, b mcp.ToolSpec) int { return strings.Compare(a.Name, b.Name) })
	return specs
}

func (f *fakeTools) Call(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = append(f.called, name)
	f.ctxs = append(f.ctxs, ctx)
	if f.args == nil {
		f.args = map[string]json.RawMessage{}
	}
	f.args[name] = args
	if err := f.errs[name]; err != nil {
		return nil, err
	}
	return json.RawMessage(f.out[name]), nil
}

type fakeWriter struct {
	plans, applies int
	change         Change
	applyErr       error
	undoErr        error
	undone         int
	rows           []string
}

func (w *fakeWriter) Plan(_ context.Context, _ Actor, tool string, in json.RawMessage) (Change, error) {
	w.plans++
	c := w.change
	c.Tool = tool
	return c, nil
}

func (w *fakeWriter) Apply(_ context.Context, _ Actor, _ Change, rows []string) (Applied, error) {
	w.applies++
	w.rows = rows
	return Applied{BackupID: uuid.New(), Rows: rows}, w.applyErr
}

func (w *fakeWriter) Undo(context.Context, Actor, Change, Applied) error {
	w.undone++
	return w.undoErr
}

type fakeSettings struct {
	off bool
	ai  store.AIChanges
}

func (f *fakeSettings) AssistantSettings(context.Context, uuid.UUID) (bool, store.AIChanges, error) {
	return f.off, f.ai, nil
}

type fakePlans struct {
	plan  string
	seats int
}

func (f fakePlans) AssistantPlan(context.Context, uuid.UUID) (string, int, error) {
	return f.plan, f.seats, nil
}

type env struct {
	svc      *Service
	st       *MemoryStore
	model    *scripted
	tools    *fakeTools
	writer   *fakeWriter
	settings *fakeSettings
	actor    Actor
	chat     uuid.UUID
	now      time.Time
}

func newEnv(t *testing.T, cfg config.AssistantConfig, rounds ...round) *env {
	t.Helper()
	if cfg.Model == "" {
		cfg.Model = "claude-sonnet-5-5"
	}
	e := &env{
		st:       NewMemoryStore(),
		model:    &scripted{rounds: rounds},
		tools:    &fakeTools{out: map[string]string{}},
		writer:   &fakeWriter{change: Change{Kind: "alerts", MinRole: store.RoleMember, Rows: []ChangeRow{{ID: "r1", Checked: true}}}},
		settings: &fakeSettings{ai: store.AIChangesAlertsAndRules},
		now:      time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC),
	}
	org, user := uuid.New(), uuid.New()
	e.actor = Actor{OrgID: org, UserID: user, Role: store.RoleAdmin, Token: store.Token{ID: uuid.New(), OrgID: org, Kind: store.TokenKindUser}}
	e.svc = NewService(cfg, e.model, e.tools, e.writer, e.st, nil, e.settings, nil)
	e.svc.now = func() time.Time { return e.now }
	e.chat = uuid.New()
	if err := e.st.CreateChat(context.Background(), Chat{ID: e.chat, OrgID: org, UserID: user, CreatedAt: e.now, UpdatedAt: e.now}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) ask(ctx context.Context, text string) ([]Event, error) {
	var evs []Event
	err := e.svc.Ask(ctx, e.actor, e.chat, Question{Text: text}, func(ev Event) error {
		evs = append(evs, ev)
		return nil
	})
	return evs, err
}

func names(evs []Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Name)
	}
	return out
}

func find(t *testing.T, evs []Event, name string) Event {
	t.Helper()
	for _, e := range evs {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no %s event in %v", name, names(evs))
	return Event{}
}

func (e *env) messages(t *testing.T) []StoredMessage {
	t.Helper()
	_, msgs, err := e.st.Chat(context.Background(), e.actor.OrgID, e.actor.UserID, e.chat)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

func TestTextOnly(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{}, round{parts: []Part{txt("Hello "), txt("world"), stop(StopEnd, 10, 5)}})
	evs, err := e.ask(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(names(evs), ","), "text,text,usage,done"; got != want {
		t.Fatalf("events %s, want %s", got, want)
	}
	var done DoneEvent
	if err := find(t, evs, EventDone).Decode(&done); err != nil {
		t.Fatal(err)
	}
	if done.Stop != StopEnd || done.Stats.Input != 10 || done.Stats.Output != 5 || done.ChatID != e.chat {
		t.Fatalf("done %+v", done)
	}
	var u UsageEvent
	_ = find(t, evs, EventUsage).Decode(&u)
	if u.Day.Used != 1 || u.Month.Used != 1 {
		t.Fatalf("usage %+v", u)
	}
	msgs := e.messages(t)
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" || msgs[1].Status != StatusDone || msgs[1].ID != done.MessageID {
		t.Fatalf("messages %+v", msgs)
	}
	if msgs[1].Native[0].Text != "Hello world" {
		t.Fatalf("native %+v", msgs[1].Native)
	}
	var te TextEvent
	_ = msgs[1].Parts[0].Decode(&te)
	if len(msgs[1].Parts) != 1 || te.Delta != "Hello world" {
		t.Fatalf("stored parts %+v", msgs[1].Parts)
	}
}

func TestReadTool(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{},
		round{parts: []Part{call("t1", "alerts", `{"range":"30d"}`), stop(StopToolUse, 10, 5)}},
		round{parts: []Part{txt("Done"), stop(StopEnd, 20, 5)}},
	)
	e.tools.out["alerts"] = `{"alerts":[1,2,3],"total":3}`
	evs, err := e.ask(context.Background(), "why?")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(names(evs), ","), "step,step,text,usage,done"; got != want {
		t.Fatalf("events %s, want %s", got, want)
	}
	var running, done StepEvent
	_ = evs[0].Decode(&running)
	_ = evs[1].Decode(&done)
	if running.Status != "running" || running.Tool != "alerts" || running.Title != "List alerts" || running.ID != "t1" {
		t.Fatalf("running %+v", running)
	}
	if done.Status != "done" || done.Rows != 3 || done.ID != "t1" {
		t.Fatalf("done %+v", done)
	}
	// The second request holds the tool result.
	msgs := e.model.reqs[1].Messages
	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.Parts[0].Kind != PartToolResult || last.Parts[0].Result.CallID != "t1" || string(last.Parts[0].Result.Content) != `{"alerts":[1,2,3],"total":3}` {
		t.Fatalf("last message %+v", last)
	}
	// The tool ctx has the token and the role.
	tok, ok := authctx.FromContext(e.tools.ctxs[0])
	role, ok2 := authctx.RoleFromContext(e.tools.ctxs[0])
	if !ok || !ok2 || tok.ID != e.actor.Token.ID || role != store.RoleAdmin {
		t.Fatalf("ctx token %v %v role %v %v", tok.ID, ok, role, ok2)
	}
	var d DoneEvent
	_ = find(t, evs, EventDone).Decode(&d)
	if d.Stats.ToolCalls != 1 || d.Stats.Rows != 3 {
		t.Fatalf("stats %+v", d.Stats)
	}
	// The step is stored once, in its final state.
	msgs2 := e.messages(t)
	var stored StepEvent
	for _, m := range msgs2 {
		for _, p := range m.Parts {
			if p.Name == EventStep {
				_ = p.Decode(&stored)
			}
		}
	}
	if stored.Status != "done" || stored.Title != "List alerts" {
		t.Fatalf("stored step %+v", stored)
	}
	if len(msgs2) != 4 {
		t.Fatalf("want 4 messages, got %d", len(msgs2))
	}
}

func TestToolError(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{},
		round{parts: []Part{call("t1", "alerts", `{}`), stop(StopToolUse, 1, 1)}},
		round{parts: []Part{txt("ok"), stop(StopEnd, 1, 1)}},
	)
	e.tools.errs = map[string]error{"alerts": errors.New("bad range")}
	evs, err := e.ask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	var s StepEvent
	_ = evs[1].Decode(&s)
	if s.Status != "error" {
		t.Fatalf("step %+v", s)
	}
	msgs := e.model.reqs[1].Messages
	res := msgs[len(msgs)-1].Parts[0].Result
	if !res.IsError || !strings.Contains(string(res.Content), "bad range") {
		t.Fatalf("result %+v", res)
	}
}

func TestWriteToolMakesProposal(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{},
		round{parts: []Part{call("t1", "ack_alert", `{"alert":"a1"}`), stop(StopToolUse, 1, 1)}},
		round{parts: []Part{txt("Proposed"), stop(StopEnd, 1, 1)}},
	)
	evs, err := e.ask(context.Background(), "ack it")
	if err != nil {
		t.Fatal(err)
	}
	var p Proposal
	if err := find(t, evs, EventProposal).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Tool != "ack_alert" || p.Status != ProposalPending || p.Kind != "alerts" || string(p.Input) != `{"alert":"a1"}` {
		t.Fatalf("proposal %+v", p)
	}
	if e.writer.plans != 1 || e.writer.applies != 0 {
		t.Fatalf("plans %d applies %d", e.writer.plans, e.writer.applies)
	}
	stored, err := e.st.Proposal(context.Background(), e.actor.OrgID, p.ID)
	if err != nil || stored.ChatID != e.chat {
		t.Fatalf("stored %+v err %v", stored, err)
	}
	msgs := e.model.reqs[1].Messages
	res := msgs[len(msgs)-1].Parts[0].Result
	if res.IsError || !strings.Contains(string(res.Content), p.ID.String()) || !strings.Contains(string(res.Content), "not applied") {
		t.Fatalf("result %s", res.Content)
	}
}

func TestWritePlanChecksRealKind(t *testing.T) {
	// mute_metric on the org is a rules change. A member must not get it.
	e := newEnv(t, config.AssistantConfig{},
		round{parts: []Part{call("t1", "mute_metric", `{}`), stop(StopToolUse, 1, 1)}},
		round{parts: []Part{txt("no"), stop(StopEnd, 1, 1)}},
	)
	e.actor.Role = store.RoleMember
	e.writer.change = Change{Kind: "rules", MinRole: store.RoleAdmin}
	evs, err := e.ask(context.Background(), "mute")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(names(evs), EventProposal) {
		t.Fatal("proposal made")
	}
	msgs := e.model.reqs[1].Messages
	if !msgs[len(msgs)-1].Parts[0].Result.IsError {
		t.Fatal("want an error result")
	}
}

func TestToolCallCap(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{Limits: config.AssistantLimits{Default: config.AssistantPlanLimits{MaxToolCalls: 1}}},
		round{parts: []Part{call("t1", "alerts", `{}`), call("t2", "alerts", `{}`), stop(StopToolUse, 1, 1)}},
		round{parts: []Part{txt("answer"), stop(StopEnd, 1, 1)}},
	)
	e.tools.out["alerts"] = `{}`
	evs, err := e.ask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.tools.called) != 1 {
		t.Fatalf("called %v", e.tools.called)
	}
	if len(e.model.reqs) != 2 || e.model.reqs[0].NoTools || !e.model.reqs[1].NoTools {
		t.Fatalf("NoTools %+v", e.model.reqs)
	}
	msgs := e.model.reqs[1].Messages
	res := msgs[len(msgs)-1].Parts
	if res[0].Result.IsError || !res[1].Result.IsError || !strings.Contains(string(res[1].Result.Content), "limit reached") {
		t.Fatalf("results %+v %+v", res[0].Result, res[1].Result)
	}
	if !slices.Contains(names(evs), EventDone) {
		t.Fatalf("events %v", names(evs))
	}
}

func TestToolCapStillCalling(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{Limits: config.AssistantLimits{Default: config.AssistantPlanLimits{MaxToolCalls: 1}}},
		round{parts: []Part{call("t1", "alerts", `{}`), stop(StopToolUse, 1, 1)}},
		round{parts: []Part{call("t2", "alerts", `{}`), stop(StopToolUse, 1, 1)}},
	)
	e.tools.out["alerts"] = `{}`
	evs, err := e.ask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	var ee ErrorEvent
	_ = find(t, evs, EventError).Decode(&ee)
	if ee.Code != CodeToolLimit {
		t.Fatalf("error %+v", ee)
	}
}

func TestQuestionTokenCap(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{Limits: config.AssistantLimits{Default: config.AssistantPlanLimits{QuestionTokens: 100}}},
		round{parts: []Part{call("t1", "alerts", `{}`), stop(StopToolUse, 200, 10)}},
	)
	evs, err := e.ask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	var ee ErrorEvent
	_ = find(t, evs, EventError).Decode(&ee)
	if ee.Code != CodeQuestionToken || len(e.model.reqs) != 1 || len(e.tools.called) != 0 {
		t.Fatalf("error %+v reqs %d called %v", ee, len(e.model.reqs), e.tools.called)
	}
	if slices.Contains(names(evs), EventDone) {
		t.Fatal("done sent after error")
	}
}

func TestCancelStoresStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := newEnv(t, config.AssistantConfig{}, round{
		parts: []Part{txt("par"), txt("tial"), stop(StopEnd, 1, 1)},
		after: cancel,
	})
	evs, err := e.ask(ctx, "q")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if got := names(evs); len(got) != 1 || got[0] != EventText {
		t.Fatalf("events %v", got)
	}
	msgs := e.messages(t)
	if len(msgs) != 2 || msgs[1].Status != StatusStopped || msgs[1].Native[0].Text != "par" {
		t.Fatalf("messages %+v", msgs)
	}
}

func TestCancelDropsDanglingCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := newEnv(t, config.AssistantConfig{}, round{
		parts: []Part{txt("Let me look. "), call("t1", "alerts", `{}`), stop(StopToolUse, 1, 1)},
		after: cancel,
	})
	_, err := e.ask(ctx, "q")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	for _, m := range e.messages(t) {
		for _, p := range m.Native {
			if p.Kind == PartToolCall {
				t.Fatalf("stored call %+v", p)
			}
		}
	}
}

func TestEmitErrorStops(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{}, round{parts: []Part{txt("a"), txt("b"), stop(StopEnd, 1, 1)}})
	boom := errors.New("client gone")
	n := 0
	err := e.svc.Ask(context.Background(), e.actor, e.chat, Question{Text: "q"}, func(Event) error {
		n++
		return boom
	})
	if !errors.Is(err, boom) || n != 1 {
		t.Fatalf("err %v, emits %d", err, n)
	}
	if msgs := e.messages(t); len(msgs) != 2 || msgs[1].Status != StatusStopped {
		t.Fatalf("messages %+v", msgs)
	}
}

func TestRefusal(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{}, round{parts: []Part{stop(StopRefusal, 1, 0)}})
	evs, err := e.ask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	var ee ErrorEvent
	_ = find(t, evs, EventError).Decode(&ee)
	if ee.Code != CodeRefused {
		t.Fatalf("error %+v", ee)
	}
	if msgs := e.messages(t); msgs[len(msgs)-1].Status == StatusDone && msgs[len(msgs)-1].Role == "assistant" {
		t.Fatalf("status %+v", msgs)
	}
}

func TestProviderError(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{}, round{parts: []Part{txt("x")}, err: &ProviderError{Status: 500, Err: errors.New("secret detail")}})
	evs, err := e.ask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	ev := find(t, evs, EventError)
	var ee ErrorEvent
	_ = ev.Decode(&ee)
	if ee.Code != CodeProviderError || strings.Contains(ee.Message, "secret") {
		t.Fatalf("error %+v", ee)
	}
}

func TestMaxTokensStopsWithoutRunningTools(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{}, round{parts: []Part{txt("cut"), call("t1", "alerts", `{`), stop(StopMaxTokens, 1, 1)}})
	evs, err := e.ask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	var d DoneEvent
	_ = find(t, evs, EventDone).Decode(&d)
	if d.Stop != StopMaxTokens || len(e.tools.called) != 0 {
		t.Fatalf("done %+v called %v", d, e.tools.called)
	}
}

func TestBadToolInputIsError(t *testing.T) {
	bad := Part{Kind: PartToolCall, Call: &ToolCall{ID: "t1", Name: "alerts", Err: "not valid JSON"}}
	e := newEnv(t, config.AssistantConfig{},
		round{parts: []Part{bad, stop(StopToolUse, 1, 1)}},
		round{parts: []Part{txt("ok"), stop(StopEnd, 1, 1)}},
	)
	if _, err := e.ask(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	msgs := e.model.reqs[1].Messages
	if res := msgs[len(msgs)-1].Parts[0].Result; !res.IsError || len(e.tools.called) != 0 {
		t.Fatalf("result %+v called %v", res, e.tools.called)
	}
}

func TestPauseTurnContinues(t *testing.T) {
	native := Part{Kind: PartNative, Native: json.RawMessage(`{"type":"server_tool_use","id":"s1","name":"web_search","input":{}}`)}
	e := newEnv(t, config.AssistantConfig{},
		round{parts: []Part{native, stop(StopPause, 1, 1)}},
		round{parts: []Part{txt("answer"), stop(StopEnd, 1, 1)}},
	)
	evs, err := e.ask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.model.reqs) != 2 || !slices.Contains(names(evs), EventDone) {
		t.Fatalf("reqs %d events %v", len(e.model.reqs), names(evs))
	}
	m := e.model.reqs[1].Messages
	if last := m[len(m)-1]; last.Role != "assistant" || last.Parts[0].Kind != PartNative {
		t.Fatalf("last %+v", last)
	}
}

func TestShowChartBar(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{},
		round{parts: []Part{
			call("bad", "show_chart", `{"kind":"bar_chart","title":"x","source":"nope","bars":[{"label":"a","value":1}]}`),
			call("r1", "alerts", `{}`),
			call("c1", "show_chart", `{"kind":"bar_chart","title":"Emails","unit":"emails","source":"r1","bars":[{"label":"cpu-high","value":96}]}`),
			stop(StopToolUse, 1, 1)}},
		round{parts: []Part{txt("see chart"), stop(StopEnd, 1, 1)}},
	)
	e.tools.out["alerts"] = `{"rows":[1]}`
	evs, err := e.ask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	msgs := e.model.reqs[1].Messages
	res := msgs[len(msgs)-1].Parts
	if !res[0].Result.IsError || !strings.Contains(string(res[0].Result.Content), "source") {
		t.Fatalf("unknown source result %+v", res[0].Result)
	}
	if res[2].Result.IsError || string(res[2].Result.Content) != `"shown"` {
		t.Fatalf("shown result %+v", res[2].Result)
	}
	var c Component
	if err := find(t, evs, EventComponent).Decode(&c); err != nil {
		t.Fatal(err)
	}
	if c.Kind != KindBarChart || c.Source != "r1" || len(c.Bars) != 1 || c.ID != "c1" {
		t.Fatalf("component %+v", c)
	}
}

func TestShowChartLineReadsHostMetrics(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{},
		round{parts: []Part{
			call("c1", "show_chart", `{"kind":"line_chart","title":"web-1 cpu","host":"web-1","metric":"cpu","thresholds":[{"label":"now 80%","value":80,"style":"current"}]}`),
			stop(StopToolUse, 1, 1)}},
		round{parts: []Part{txt("ok"), stop(StopEnd, 1, 1)}},
	)
	e.tools.out["host_metrics"] = `{"from":"a","to":"b","resolution":"5m","hosts":[{"host":"web-1","host_id":"h1","metrics":[{"metric":"cpu","unit":"%","count":2,"points":[{"ts":"2026-11-25T00:00:00Z","value":41.5},{"ts":"2026-11-25T00:05:00Z","value":42}]}]}]}`
	evs, err := e.ask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	var c Component
	_ = find(t, evs, EventComponent).Decode(&c)
	if c.Kind != KindLineChart || c.Unit != "%" || len(c.Series) != 1 || len(c.Series[0].Points) != 2 || c.Series[0].Points[0][1] != 41.5 || len(c.Thresholds) != 1 {
		t.Fatalf("component %+v", c)
	}
	if c.Series[0].Points[0][0] != float64(time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC).Unix()) {
		t.Fatalf("point time %v", c.Series[0].Points[0][0])
	}
	var args map[string]any
	_ = json.Unmarshal(e.tools.args["host_metrics"], &args)
	if args["host"] != "web-1" || args["range"] != "7d" {
		t.Fatalf("args %v", args)
	}
	if _, ok := authctx.FromContext(e.tools.ctxs[0]); !ok {
		t.Fatal("host_metrics ctx has no token")
	}
}

func TestCite(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{},
		round{parts: []Part{
			call("r1", "alerts", `{}`),
			call("c1", "cite", `{"label":"alerts","source":"r1","rows":["a1"],"href":"/hosts/h_1?tab=processes"}`),
			call("c2", "cite", `{"label":"x","source":"r1","rows":["a1"],"href":"https://evil.example"}`),
			stop(StopToolUse, 1, 1)}},
		round{parts: []Part{txt("ok"), stop(StopEnd, 1, 1)}},
	)
	e.tools.out["alerts"] = `[]`
	evs, err := e.ask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	var c Component
	_ = find(t, evs, EventComponent).Decode(&c)
	if c.Kind != KindCitation || c.Href != "/hosts/h_1?tab=processes" {
		t.Fatalf("component %+v", c)
	}
	msgs := e.model.reqs[1].Messages
	if !msgs[len(msgs)-1].Parts[2].Result.IsError {
		t.Fatal("external href accepted")
	}
}

func TestWriteToolsByRoleAndRule(t *testing.T) {
	cases := []struct {
		role store.Role
		ai   store.AIChanges
		want []string
	}{
		{store.RoleViewer, store.AIChangesAlertsAndRules, nil},
		{store.RoleMember, store.AIChangesAlerts, []string{"ack_alert", "mute_metric", "snooze_alert", "snooze_host"}},
		{store.RoleMember, store.AIChangesAlertsAndRules, []string{"ack_alert", "mute_metric", "snooze_alert", "snooze_host"}},
		{store.RoleAdmin, store.AIChangesAlerts, []string{"ack_alert", "mute_metric", "snooze_alert", "snooze_host"}},
		{store.RoleAdmin, store.AIChangesAlertsAndRules, writeNames},
		{store.RoleAdmin, store.AIChangesOff, nil},
	}
	for _, tc := range cases {
		t.Run(string(tc.role)+"/"+string(tc.ai), func(t *testing.T) {
			e := newEnv(t, config.AssistantConfig{}, round{parts: []Part{txt("x"), stop(StopEnd, 1, 1)}})
			e.actor.Role, e.settings.ai = tc.role, tc.ai
			if _, err := e.ask(context.Background(), "q"); err != nil {
				t.Fatal(err)
			}
			var got []string
			var all []string
			for _, d := range e.model.reqs[0].Tools {
				all = append(all, d.Name)
				if _, w := writeKinds[d.Name]; w {
					got = append(got, d.Name)
				}
			}
			want := slices.Sorted(slices.Values(tc.want))
			if !slices.Equal(got, want) {
				t.Fatalf("write tools %v, want %v", got, want)
			}
			for _, n := range []string{"alerts", "host_metrics", toolShowChart, toolCite} {
				if !slices.Contains(all, n) {
					t.Fatalf("missing %s in %v", n, all)
				}
			}
			if !slices.IsSortedFunc(e.model.reqs[0].Tools, func(a, b ToolDef) int { return strings.Compare(a.Name, b.Name) }) {
				t.Fatal("tools not sorted")
			}
		})
	}
}

func TestRequestFromPlan(t *testing.T) {
	yes := true
	cfg := config.AssistantConfig{
		Model: "claude-sonnet-5-5", MaxTokens: 1000, WebSearch: false,
		Limits: config.AssistantLimits{Plans: map[string]config.AssistantPlanLimits{
			"team": {Model: "claude-haiku-5-5", ReplyTokens: 321, WebSearch: &yes},
		}},
	}
	e := newEnv(t, cfg, round{parts: []Part{txt("x"), stop(StopEnd, 1, 1)}})
	e.svc.plans = fakePlans{plan: "team", seats: 3}
	if _, err := e.ask(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	r := e.model.reqs[0]
	if r.Model != "claude-haiku-5-5" || r.MaxTokens != 321 || !r.WebSearch || r.Effort != "low" || r.NoTools {
		t.Fatalf("request %+v", r)
	}
	if len(r.Conversation) != 32 || strings.Contains(r.Conversation, "-") {
		t.Fatalf("conversation %q", r.Conversation)
	}
	if len(r.System) < 2 || !strings.Contains(r.System[0], "Towertail assistant") {
		t.Fatalf("system %d", len(r.System))
	}
	if first := r.Messages[0]; !strings.Contains(first.Parts[0].Text, "Time now: 2026-11-25T10:00:00Z") {
		t.Fatalf("first message %q", first.Parts[0].Text)
	}
}

func TestLimits(t *testing.T) {
	cfg := config.AssistantConfig{Limits: config.AssistantLimits{Default: config.AssistantPlanLimits{QuestionsPerDay: 1, QuestionsPerSeatMonth: 2}}}
	e := newEnv(t, cfg,
		round{parts: []Part{txt("x"), stop(StopEnd, 1, 1)}},
		round{parts: []Part{txt("x"), stop(StopEnd, 1, 1)}},
		round{parts: []Part{txt("x"), stop(StopEnd, 1, 1)}},
	)
	e.svc.plans = fakePlans{seats: 1}
	if _, err := e.ask(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	_, err := e.ask(context.Background(), "two")
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != CodeDailyLimit || !ae.ResetAt.Equal(time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("day limit: %v", err)
	}
	e.now = e.now.Add(24 * time.Hour)
	if _, err := e.ask(context.Background(), "three"); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(24 * time.Hour)
	_, err = e.ask(context.Background(), "four")
	if !errors.As(err, &ae) || ae.Code != CodePlanLimit || !ae.ResetAt.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("month limit: %v", err)
	}
	v, err := e.svc.Usage(context.Background(), e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if v.Month.Limit != 2 || !v.Month.Pooled || v.Day.Limit != 1 || v.Model != "claude-sonnet-5-5" || v.ModelName != "Sonnet 5.5" || !v.Enabled {
		t.Fatalf("usage %+v", v)
	}
	if len(e.model.reqs) != 2 {
		t.Fatalf("model got %d requests", len(e.model.reqs))
	}
}

func TestAssistantOff(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{})
	e.settings.off = true
	_, err := e.ask(context.Background(), "q")
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != CodeAssistantOff || len(e.model.reqs) != 0 {
		t.Fatalf("err %v", err)
	}
	v, _ := e.svc.Usage(context.Background(), e.actor)
	if !v.Off {
		t.Fatalf("usage %+v", v)
	}
}

func TestBadQuestion(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{})
	if _, err := e.ask(context.Background(), "  "); !errors.Is(err, ErrEmptyQuestion) {
		t.Fatalf("err %v", err)
	}
	if _, err := e.ask(context.Background(), strings.Repeat("x", maxQuestionRunes+1)); !errors.Is(err, ErrQuestionTooLong) {
		t.Fatalf("err %v", err)
	}
	if v, _ := e.svc.Usage(context.Background(), e.actor); v.Day.Used != 0 {
		t.Fatalf("bad question used quota: %+v", v)
	}
}

func TestOtherUsersChat(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{})
	e.actor.UserID = uuid.New()
	if _, err := e.ask(context.Background(), "q"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err %v", err)
	}
}

func TestHistoryReplay(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{},
		round{parts: []Part{call("t1", "alerts", `{}`), stop(StopToolUse, 1, 1)}},
		round{parts: []Part{txt("first"), stop(StopEnd, 1, 1)}},
		round{parts: []Part{txt("second"), stop(StopEnd, 1, 1)}},
	)
	e.tools.out["alerts"] = `{}`
	if _, err := e.ask(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ask(context.Background(), "two"); err != nil {
		t.Fatal(err)
	}
	m := e.model.reqs[2].Messages
	var roles []string
	for _, x := range m {
		roles = append(roles, x.Role)
	}
	if got := strings.Join(roles, ","); got != "user,assistant,user,assistant,user" {
		t.Fatalf("roles %s", got)
	}
	if c := e.model.reqs[0].Conversation; c == "" || e.model.reqs[1].Conversation != c || e.model.reqs[2].Conversation != c {
		t.Fatal("conversation changes in one chat")
	}
}

func TestHistoryDropsDanglingCalls(t *testing.T) {
	stored := []StoredMessage{
		{Role: "user", Native: []Part{txt("q")}},
		{Role: "assistant", Native: []Part{txt("looking"), call("t1", "alerts", `{}`), {Kind: PartNative, Native: json.RawMessage(`{"type":"tool_use","id":"t1"}`)}}},
		{Role: "user", Native: []Part{txt("next")}},
	}
	m := historyOf(stored)
	if len(m) != 3 {
		t.Fatalf("messages %+v", m)
	}
	for _, p := range m[1].Parts {
		if p.Kind == PartToolCall || p.Kind == PartNative {
			t.Fatalf("dangling part left: %+v", p)
		}
	}
}

func TestFitKeepsLastTurn(t *testing.T) {
	big := strings.Repeat("x", 4000)
	msgs := []Message{
		{Role: "user", Parts: []Part{txt(big)}},
		{Role: "assistant", Parts: []Part{txt(big)}},
		{Role: "user", Parts: []Part{txt(big)}},
		{Role: "assistant", Parts: []Part{txt(big)}},
		{Role: "user", Parts: []Part{txt(big)}},
	}
	if got := fit(msgs, 1500); len(got) != 1 || got[0].Role != "user" {
		t.Fatalf("got %d messages", len(got))
	}
	if got := fit(msgs, 3500); len(got) != 3 {
		t.Fatalf("got %d messages", len(got))
	}
	if got := fit(msgs, 1_000_000); len(got) != 5 {
		t.Fatalf("got %d messages", len(got))
	}
}

func TestModelName(t *testing.T) {
	for in, want := range map[string]string{
		"claude-sonnet-5-5": "Sonnet 5.5", "claude-opus-5": "Opus 5", "gpt-5": "gpt-5", "claude-3-5-sonnet": "claude-3-5-sonnet",
	} {
		if got := modelName(in); got != want {
			t.Errorf("modelName(%q) = %q, want %q", in, got, want)
		}
	}
}
