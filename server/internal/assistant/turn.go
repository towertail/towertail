package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
)

const (
	maxRounds        = 40
	maxQuestionRunes = 8000
	persistTimeout   = 10 * time.Second
	// CodeInternal is the error code of a failure that is not the provider.
	CodeInternal = "internal"
)

var (
	// ErrEmptyQuestion means that the question has no text.
	ErrEmptyQuestion = errors.New("assistant: empty question")
	// ErrQuestionTooLong means that the question text is over the limit.
	ErrQuestionTooLong = errors.New("assistant: question too long")
	errAborted         = errors.New("assistant: aborted")
)

// Ask runs one question and calls emit for each event, in order. It
// returns nil after the done event or after an error event. A failure
// before the first event comes back as an *Error with no event. When ctx
// ends or emit fails, Ask keeps the partial answer as "stopped", sends
// no more events and returns the cause.
func (s *Service) Ask(ctx context.Context, a Actor, chatID uuid.UUID, q Question, emit func(Event) error) error {
	q.Text = strings.TrimSpace(q.Text)
	switch {
	case q.Text == "":
		return ErrEmptyQuestion
	case utf8.RuneCountInString(q.Text) > maxQuestionRunes:
		return ErrQuestionTooLong
	case s.model == nil || s.tools == nil || s.store == nil:
		return &Error{Code: CodeAssistantOff, Message: "The assistant is not available."}
	}
	off, ai, err := s.orgSettings(ctx, a.OrgID)
	if err != nil {
		return err
	}
	if off {
		return &Error{Code: CodeAssistantOff, Message: "An admin turned the assistant off for this organization."}
	}
	_, stored, err := s.store.Chat(ctx, a.OrgID, a.UserID, chatID)
	if err != nil {
		return err
	}
	l, err := s.limitsFor(ctx, a.OrgID)
	if err != nil {
		return err
	}
	now := s.now()
	month, _ := l.perMonth()
	view, err := s.store.Reserve(ctx, Reservation{OrgID: a.OrgID, UserID: a.UserID, At: now.UTC(), PerDay: l.lim.QuestionsPerDay, PerMonth: month})
	if err != nil {
		var le *LimitError
		if errors.As(err, &le) {
			if le.Scope == "day" {
				return &Error{Code: CodeDailyLimit, Message: "You reached your daily limit of questions.", ResetAt: le.ResetAt}
			}
			return &Error{Code: CodePlanLimit, Message: "Your organization reached the monthly limit of questions of its plan.", ResetAt: le.ResetAt}
		}
		return err
	}
	t := &turn{
		s: s, a: a, l: l, ai: ai, chatID: chatID, emit: emit, start: now,
		system: systemPrompt(l.cfg), tb: s.newToolbox(a, ai),
		calls: map[string]string{}, steps: map[string]int{},
	}
	return t.run(ctx, q, historyOf(stored), view)
}

// turn is the state of one question.
type turn struct {
	s      *Service
	a      Actor
	l      orgLimits
	ai     store.AIChanges
	chatID uuid.UUID
	emit   func(Event) error
	start  time.Time
	system []string
	tb     *toolbox

	hist  []Message         // sent to the model
	out   []StoredMessage   // new messages, in order
	calls map[string]string // read tool calls that worked: ID to tool

	events  []Event        // events of the current assistant message
	steps   map[string]int // index in events of the step of a call
	pending strings.Builder

	emitErr error
	usage   Usage
	tokens  int
	nCalls  int
	nComps  int
	stats   DoneStats
}

func (t *turn) run(ctx context.Context, q Question, hist []Message, view UsageView) error {
	user := Message{Role: "user", Parts: []Part{{Kind: PartText, Text: userContext("", t.start, q)}}}
	t.hist = fit(append(hist, user), t.l.cfg.ContextTokensOrDefault())
	t.out = append(t.out, StoredMessage{
		ID: uuid.New(), ChatID: t.chatID, Role: "user", Native: user.Parts, Status: StatusDone, CreatedAt: t.start,
		Parts: []Event{newEvent(EventQuestion, QuestionEvent{Text: q.Text, Context: q.Context, Mentions: q.Mentions})},
	})

	stop, err := t.loop(ctx)
	var fail *Error
	if errors.Is(err, errAborted) {
		if _, err := t.save(ctx, StatusStopped); err != nil {
			t.s.log.Error("assistant: save stopped answer", "err", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return t.emitErr
	}
	if errors.As(err, &fail) {
		if _, err := t.save(ctx, StatusError); err != nil {
			t.s.log.Error("assistant: save failed answer", "err", err)
		}
		t.send(newEvent(EventError, ErrorEvent{Code: fail.Code, Message: fail.Message, ResetAt: fail.ResetAt}))
		return t.emitErr
	}
	id, err := t.save(ctx, StatusDone)
	if err != nil {
		t.s.log.Error("assistant: save answer", "err", err)
		t.send(newEvent(EventError, ErrorEvent{Code: CodeInternal, Message: "The answer could not be saved."}))
		return t.emitErr
	}
	t.send(newEvent(EventUsage, UsageEvent{Day: view.Day, Month: view.Month}))
	t.stats.MS = t.s.now().Sub(t.start).Milliseconds()
	t.stats.Input, t.stats.Output, t.stats.CacheRead = t.usage.Input, t.usage.Output, t.usage.CacheRead
	t.send(newEvent(EventDone, DoneEvent{ChatID: t.chatID, MessageID: id, Stop: stop, Stats: t.stats}))
	return t.emitErr
}

// reply is one streamed model answer.
type reply struct {
	parts []Part
	calls []ToolCall
	stop  StopReason
	usage Usage
}

func (t *turn) loop(ctx context.Context) (StopReason, error) {
	capped := false
	for round := 0; ; round++ {
		last := round >= maxRounds-1
		r, err := t.stream(ctx, Request{
			Model: t.l.model, System: t.system, Tools: t.tb.defs, Messages: t.hist,
			MaxTokens: t.l.maxTokens(), WebSearch: t.l.webSearch(), Effort: t.l.cfg.EffortOrDefault(),
			NoTools: capped || last,
		})
		if err != nil {
			t.keep(r.parts, r.usage, true)
			if t.aborted(ctx) {
				return "", errAborted
			}
			t.s.log.Error("assistant: model request failed", "err", err)
			return "", &Error{Code: CodeProviderError, Message: "The model request failed. Try again later."}
		}
		t.usage.Add(r.usage)
		t.tokens += r.usage.Input + r.usage.Output + r.usage.CacheRead + r.usage.CacheWrite

		switch r.stop {
		case StopRefusal:
			t.keep(r.parts, r.usage, true)
			return "", &Error{Code: CodeRefused, Message: "The model refused to answer this question."}
		case StopMaxTokens:
			t.keep(r.parts, r.usage, true)
			return StopMaxTokens, nil
		case StopEnd:
			t.keep(r.parts, r.usage, false)
			return StopEnd, nil
		}
		if q := t.l.lim.QuestionTokens; q > 0 && t.tokens >= q {
			t.keep(r.parts, r.usage, true)
			return "", &Error{Code: CodeQuestionToken, Message: "This question used the token limit of your plan. Ask a narrower question."}
		}
		if r.stop == StopPause {
			t.keep(r.parts, r.usage, false)
			continue
		}
		if len(r.calls) == 0 {
			t.keep(r.parts, r.usage, false)
			return StopEnd, nil
		}
		if capped || last {
			t.keep(r.parts, r.usage, true)
			return "", &Error{Code: CodeToolLimit, Message: "This question needed more tool calls than your plan allows."}
		}

		results := make([]Part, 0, len(r.calls))
		for _, c := range r.calls {
			if t.aborted(ctx) {
				t.keep(r.parts, r.usage, true)
				return "", errAborted
			}
			res := t.exec(ctx, c)
			results = append(results, Part{Kind: PartToolResult, Result: &res})
		}
		if t.aborted(ctx) {
			t.keep(r.parts, r.usage, true)
			return "", errAborted
		}
		t.keep(r.parts, r.usage, false)
		t.hist = append(t.hist, Message{Role: "user", Parts: results})
		t.out = append(t.out, StoredMessage{ID: uuid.New(), ChatID: t.chatID, Role: "user", Native: results, Status: StatusDone, CreatedAt: t.s.now()})
		if m := t.l.lim.MaxToolCalls; m > 0 && t.nCalls >= m {
			capped = true
		}
	}
}

// stream sends one request and gathers the answer. It sends the text
// deltas as events. On an error it still gives the parts that came before.
func (t *turn) stream(ctx context.Context, req Request) (reply, error) {
	var r reply
	for part, err := range t.s.model.Stream(ctx, req) {
		if err != nil {
			return r, err
		}
		if t.aborted(ctx) {
			return r, errAborted
		}
		switch part.Kind {
		case PartText:
			t.pending.WriteString(part.Text)
			t.send(newEvent(EventText, TextEvent{Delta: part.Text}))
			if n := len(r.parts); n > 0 && r.parts[n-1].Kind == PartText {
				r.parts[n-1].Text += part.Text
			} else {
				r.parts = append(r.parts, Part{Kind: PartText, Text: part.Text})
			}
		case PartToolCall:
			r.calls = append(r.calls, *part.Call)
			r.parts = append(r.parts, part)
		case PartNative:
			r.parts = append(r.parts, part)
		case PartStop:
			r.stop = part.Stop
			if part.Usage != nil {
				r.usage = *part.Usage
			}
		}
	}
	return r, nil
}

func (t *turn) aborted(ctx context.Context) bool { return ctx.Err() != nil || t.emitErr != nil }

func (t *turn) send(ev Event) {
	if t.emitErr != nil {
		return
	}
	t.emitErr = t.emit(ev)
}

// flushText moves the text of the current message to one stored event.
func (t *turn) flushText() {
	if t.pending.Len() > 0 {
		t.events = append(t.events, newEvent(EventText, TextEvent{Delta: t.pending.String()}))
		t.pending.Reset()
	}
}

// record sends an event and keeps it with the current message.
func (t *turn) record(ev Event) {
	t.flushText()
	t.events = append(t.events, ev)
	t.send(ev)
}

// keep ends the current assistant message. A message with dangling tool
// calls loses the calls, and the turn ends.
func (t *turn) keep(parts []Part, u Usage, dangling bool) {
	t.flushText()
	if dangling {
		parts = stripCalls(parts)
		if !slices.ContainsFunc(parts, func(p Part) bool { return p.Kind == PartText && p.Text != "" }) {
			parts = nil
		}
		if parts == nil && len(t.events) == 0 {
			return
		}
	} else {
		t.hist = append(t.hist, Message{Role: "assistant", Parts: parts})
	}
	t.out = append(t.out, StoredMessage{
		ID: uuid.New(), ChatID: t.chatID, Role: "assistant", Native: parts, Parts: t.events,
		Model: t.l.model, Usage: &u, Status: StatusDone, CreatedAt: t.s.now(),
	})
	t.events, t.steps = nil, map[string]int{}
}

// save stores the new messages. The last assistant message gets status.
// It gives the ID of that message.
func (t *turn) save(ctx context.Context, status string) (uuid.UUID, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	id := t.out[len(t.out)-1].ID
	for i := len(t.out) - 1; i >= 0; i-- {
		if t.out[i].Role == "assistant" {
			t.out[i].Status, id = status, t.out[i].ID
			break
		}
	}
	for _, m := range t.out {
		if err := t.s.store.AppendMessage(ctx, m); err != nil {
			return uuid.Nil, err
		}
	}
	if err := t.s.store.AddUsage(ctx, t.a.OrgID, t.a.UserID, t.start, t.tokens, t.stats.ToolCalls); err != nil {
		t.s.log.Error("assistant: add usage", "err", err)
	}
	return id, nil
}

// exec runs one tool call of the model.
func (t *turn) exec(ctx context.Context, c ToolCall) ToolResult {
	fail := func(msg string) ToolResult {
		return ToolResult{CallID: c.ID, Content: jsonString(msg), IsError: true}
	}
	ok := func(content json.RawMessage) ToolResult { return ToolResult{CallID: c.ID, Content: content} }
	if c.Err != "" {
		return fail(c.Err)
	}
	if m := t.l.lim.MaxToolCalls; m > 0 && t.nCalls >= m {
		return fail("Tool call limit reached. Answer now with what you have.")
	}
	t.nCalls++

	switch {
	case t.tb.component(c.Name):
		var comp Component
		var err error
		if c.Name == toolShowChart {
			comp, err = t.chart(ctx, c.Input)
		} else {
			comp, err = t.citation(c.Input)
		}
		if err != nil {
			return fail(err.Error())
		}
		t.nComps++
		comp.ID = "c" + strconv.Itoa(t.nComps)
		t.record(newEvent(EventComponent, comp))
		return ok(jsonString("shown"))

	case t.tb.write[c.Name]:
		ch, err := t.s.writer.Plan(t.a.ctx(ctx), t.a, c.Name, c.Input)
		if err != nil {
			t.s.log.Warn("assistant: plan failed", "tool", c.Name, "err", err)
			return fail(err.Error())
		}
		switch {
		case !t.a.Role.Allows(ch.MinRole):
			return fail("The role of the user does not allow this change.")
		case !permits(t.ai, ch.Kind):
			return fail("The org setting Changes by AI does not allow this change.")
		}
		if len(ch.Input) == 0 {
			ch.Input = c.Input
		}
		p := Proposal{ID: uuid.New(), OrgID: t.a.OrgID, ChatID: t.chatID, Change: ch, Status: ProposalPending, CreatedAt: t.s.now()}
		if err := t.s.store.SaveProposal(ctx, p); err != nil {
			t.s.log.Error("assistant: save proposal", "err", err)
			return fail("The proposal could not be saved.")
		}
		t.record(newEvent(EventProposal, p))
		return ok(jsonString("Proposal " + p.ID.String() + " is shown to the user. It is not applied."))
	}

	spec, found := t.tb.read[c.Name]
	if !found {
		return fail("Unknown tool " + c.Name + ".")
	}
	step := StepEvent{ID: c.ID, Tool: c.Name, Title: spec.Title, Status: "running"}
	t.steps[c.ID] = len(t.events)
	t.record(newEvent(EventStep, step))
	began := time.Now()
	out, err := t.s.tools.Call(t.a.ctx(ctx), c.Name, c.Input)
	step.MS = time.Since(began).Milliseconds()
	if err != nil {
		step.Status = "error"
		t.finishStep(step)
		t.s.log.Warn("assistant: tool failed", "tool", c.Name, "err", err)
		return fail(err.Error())
	}
	if len(out) == 0 {
		out = json.RawMessage(`{}`)
	}
	step.Status, step.Rows = "done", countRows(out)
	t.finishStep(step)
	t.calls[c.ID] = c.Name
	t.stats.ToolCalls++
	t.stats.Rows += step.Rows
	return ok(out)
}

// finishStep sends the end of a step and stores it in place of the start.
func (t *turn) finishStep(s StepEvent) {
	t.send(newEvent(EventStep, StepEvent{ID: s.ID, Status: s.Status, MS: s.MS, Rows: s.Rows}))
	t.events[t.steps[s.ID]] = newEvent(EventStep, s)
}

// countRows counts the items of the top-level arrays of a tool result.
func countRows(raw json.RawMessage) int {
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		return len(arr)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return 0
	}
	n := 0
	for _, v := range obj {
		if len(v) > 0 && v[0] == '[' {
			var a []json.RawMessage
			if json.Unmarshal(v, &a) == nil {
				n += len(a)
			}
		}
	}
	return n
}

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// stripCalls removes the tool calls, and the native blocks of them.
func stripCalls(parts []Part) []Part {
	return slices.DeleteFunc(slices.Clone(parts), func(p Part) bool {
		switch p.Kind {
		case PartToolCall:
			return true
		case PartNative:
			var h struct {
				Type string `json:"type"`
			}
			return json.Unmarshal(p.Native, &h) == nil && h.Type == "tool_use"
		}
		return false
	})
}

// historyOf gives the messages for the model. A message pair with tool
// calls that have no full results loses the calls. Messages of one role
// that follow each other join.
func historyOf(stored []StoredMessage) []Message {
	var out []Message
	for i, m := range stored {
		parts := m.Native
		if m.Role == "assistant" && !answered(parts, stored[i+1:]) {
			parts = stripCalls(parts)
		}
		if len(parts) == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Parts = append(slices.Clone(out[n-1].Parts), parts...)
			continue
		}
		out = append(out, Message{Role: m.Role, Parts: slices.Clone(parts)})
	}
	return out
}

// answered tells if the next message has a result for each call.
func answered(parts []Part, next []StoredMessage) bool {
	var ids []string
	for _, p := range parts {
		if p.Kind == PartToolCall {
			ids = append(ids, p.Call.ID)
		}
	}
	if len(ids) == 0 {
		return true
	}
	if len(next) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, p := range next[0].Native {
		if p.Kind == PartToolResult {
			have[p.Result.CallID] = true
		}
	}
	return !slices.ContainsFunc(ids, func(id string) bool { return !have[id] })
}

// fit drops the oldest turns until the estimate is within the budget. It
// always keeps the last turn.
func fit(msgs []Message, budget int) []Message {
	var starts []int
	total := 0
	sizes := make([]int, len(msgs))
	for i, m := range msgs {
		sizes[i] = estimate(m)
		total += sizes[i]
		if m.Role == "user" && slices.ContainsFunc(m.Parts, func(p Part) bool { return p.Kind == PartText }) {
			starts = append(starts, i)
		}
	}
	cut := 0
	for k := 1; k < len(starts) && total > budget; k++ {
		for ; cut < starts[k]; cut++ {
			total -= sizes[cut]
		}
	}
	return msgs[cut:]
}

// estimate gives the size of a message in tokens, at 4 bytes for a token.
func estimate(m Message) int {
	native, other := 0, 0
	for _, p := range m.Parts {
		switch p.Kind {
		case PartNative:
			native += len(p.Native)
		case PartText:
			other += len(p.Text)
		case PartToolCall:
			other += len(p.Call.Name) + len(p.Call.Input)
		case PartToolResult:
			other += len(p.Result.Content)
		}
	}
	return max(native, other) / 4
}
