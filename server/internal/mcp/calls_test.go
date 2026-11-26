package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
)

type fakeCallLog struct {
	mu   sync.Mutex
	rows []clickhouse.AICall
}

func (f *fakeCallLog) Record(c clickhouse.AICall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, c)
}

func (f *fakeCallLog) only(t *testing.T) clickhouse.AICall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rows) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(f.rows), f.rows)
	}
	return f.rows[0]
}

func loggedSession(t *testing.T, tok store.Token) (*sdk.ClientSession, *fakeCallLog) {
	t.Helper()
	log := &fakeCallLog{}
	cs := connectOpts(t, newFake(), &tok, func(o *Options) { o.Calls = log })
	return cs, log
}

func TestRecordCalls(t *testing.T) {
	userID, grant := uuid.New(), uuid.New()
	tok := store.Token{ID: uuid.New(), OrgID: orgA, Kind: store.TokenKindOAuth, Label: "Claude", UserID: &userID, GrantID: &grant}
	cs, log := loggedSession(t, tok)

	// A good call: status, tool, client, token, rows and size.
	call(t, cs, "list_hosts", nil)
	r := log.only(t)
	if r.Status != "ok" || r.Tool != "list_hosts" || r.Client != "Claude" || r.Via != "oauth" || r.Rows != 2 || r.Bytes == 0 ||
		r.OrgID != orgA || r.TokenID != tok.ID || r.UserID == nil || *r.UserID != userID || r.GrantID == nil || *r.GrantID != grant ||
		r.CallID == uuid.Nil || r.TS.IsZero() || r.Change != "" || r.Clamped || r.Error != "" {
		t.Fatalf("row = %+v", r)
	}
	// The row has no value of the result (the fake CPU is 42.5).
	if strings.Contains(fmt.Sprintf("%+v", r), "42.5") {
		t.Errorf("row has a result value: %+v", r)
	}

	// An input error gives status error and the text.
	log.rows = nil
	res := call(t, cs, "get_host", map[string]any{"host": "nope"})
	if !res.IsError {
		t.Fatal("no tool error")
	}
	r = log.only(t)
	if r.Status != "error" || r.Error == "" || r.Rows != 0 || r.Input != `{"host":"nope"}` {
		t.Fatalf("error row = %+v", r)
	}

	// clamped in the output sets Clamped.
	log.rows = nil
	call(t, cs, "host_metrics", map[string]any{"host": "web-1", "metrics": []string{"cpu"}, "range": "6h"})
	if r = log.only(t); !r.Clamped || r.Status != "ok" || r.Rows != 1 {
		t.Fatalf("clamped row = %+v", r)
	}

	// An input over 1 KB is cut.
	log.rows = nil
	call(t, cs, "search", map[string]any{"query": strings.Repeat("é", 2000)})
	if r = log.only(t); len(r.Input) > 1024 || len(r.Input) < 1000 || !strings.HasPrefix(r.Input, `{"query":"é`) {
		t.Fatalf("input = %d bytes", len(r.Input))
	}
}

func TestRecordCallsChange(t *testing.T) {
	e := newWriteEnv(t, store.AIChangesAlerts)
	log := &fakeCallLog{}
	e.srv.calls = log
	cs := e.member(t)

	// A write tool gives the change key. A read tool gives none.
	res := call(t, cs, "ack_alert", map[string]any{"alert_id": e.open.String()})
	if res.IsError {
		t.Fatalf("ack_alert: %s", text(res))
	}
	r := log.only(t)
	var ch Change
	if err := json.Unmarshal([]byte(r.Change), &ch); err != nil || len(ch.Targets) != 1 || ch.Targets[0].ID != e.open.String() || r.Status != "ok" {
		t.Fatalf("row = %+v, change err = %v", r, err)
	}
	log.rows = nil
	call(t, cs, "whoami", nil)
	if r = log.only(t); r.Change != "" || r.Status != "ok" {
		t.Fatalf("whoami row = %+v", r)
	}
	// With a call log, the write call has no audit line.
	if strings.Contains(e.logs.String(), "audit") {
		t.Errorf("audit line with a call log:\n%s", e.logs.String())
	}
}

func TestRecordCallsDenied(t *testing.T) {
	check := func(t *testing.T, e *writeEnv, cs *sdk.ClientSession, tool string, args map[string]any, want string) {
		t.Helper()
		log := &fakeCallLog{}
		e.srv.calls = log
		if res := call(t, cs, tool, args); !res.IsError {
			t.Fatalf("%s: no tool error", tool)
		}
		if r := log.only(t); r.Status != want || r.Error == "" || r.Change != "" {
			t.Fatalf("%s: row = %+v, want status %s", tool, r, want)
		}
	}
	alert := func(e *writeEnv) map[string]any { return map[string]any{"alert_id": e.open.String()} }

	e := newWriteEnv(t, store.AIChangesAlerts)
	// No scope: an oauth token with no write scope.
	check(t, e, e.session(t, e.token(), store.RoleMember), "ack_alert", alert(e), "denied")
	// Role too low for a rules change.
	check(t, e, e.session(t, e.token(bothScopes...), store.RoleMember), "set_thresholds",
		map[string]any{"metric": "cpu", "warn": 0.7, "critical": 0.9}, "denied")
	// Org setting off.
	off := newWriteEnv(t, store.AIChangesOff)
	check(t, off, off.member(t), "ack_alert", alert(off), "denied")
	// Another error is not a denial.
	check(t, e, e.member(t), "ack_alert", map[string]any{"alert_id": "nope"}, "error")
}

func TestRightErrorsWrapDenied(t *testing.T) {
	for _, err := range []error{ErrNoScope, ErrRole, ErrAIChangesOff} {
		log := &fakeCallLog{}
		s := newServer(newFake())
		s.calls = log
		ctx := ctxOf(*tokenA(), store.RoleViewer)
		s.record(ctx, "ack_alert", nil, now, nil, fmt.Errorf("wrapped: %w", err))
		if r := log.only(t); r.Status != "denied" {
			t.Errorf("%v: status = %s", err, r.Status)
		}
	}
}

func TestCallRecordsAssistant(t *testing.T) {
	log := &fakeCallLog{}
	s := newServer(newFake())
	s.calls = log
	ctx := WithVia(callerCtx(), "assistant")

	if _, err := s.Call(ctx, "list_hosts", nil); err != nil {
		t.Fatal(err)
	}
	r := log.only(t)
	if r.Via != "assistant" || r.Client != "assistant" || r.Tool != "list_hosts" || r.Status != "ok" || r.Rows != 2 {
		t.Fatalf("row = %+v", r)
	}

	log.rows = nil
	if _, err := s.Call(ctx, "list_hosts", json.RawMessage(`{"sort":5}`)); !errors.Is(err, ErrBadInput) {
		t.Fatalf("err = %v, want ErrBadInput", err)
	}
	if r = log.only(t); r.Status != "error" || r.Error == "" || r.Input != `{"sort":5}` {
		t.Fatalf("bad input row = %+v", r)
	}

	for name, want := range map[string]error{"nope": ErrUnknownTool, "ack_alert": ErrNotReadOnly} {
		log.rows = nil
		if _, err := s.Call(ctx, name, nil); !errors.Is(err, want) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if r = log.only(t); r.Status != "error" || r.Tool != name {
			t.Fatalf("%s: row = %+v", name, r)
		}
	}

	// Without WithVia, the token kind decides.
	log.rows = nil
	if _, err := s.Call(callerCtx(), "whoami", nil); err != nil {
		t.Fatal(err)
	}
	if r = log.only(t); r.Via != "api_key" {
		t.Fatalf("via = %s, want api_key", r.Via)
	}
}

func TestViaOf(t *testing.T) {
	for kind, want := range map[store.TokenKind]string{
		store.TokenKindOAuth: "oauth", store.TokenKindAPI: "api_key", store.TokenKindUser: "user", store.TokenKindAdmin: "user",
	} {
		if got := viaOf(kind); got != want {
			t.Errorf("viaOf(%s) = %s, want %s", kind, got, want)
		}
	}
}

func TestNoCallLog(t *testing.T) {
	// With no CallLog, a call works and records nothing.
	cs := connect(t, newFake(), tokenA())
	if res := call(t, cs, "list_hosts", nil); res.IsError {
		t.Fatal(text(res))
	}
	if _, err := newServer(newFake()).Call(callerCtx(), "list_hosts", nil); err != nil {
		t.Fatal(err)
	}
}

func TestGroupCoversAllTools(t *testing.T) {
	s := newServer(newFake())
	tools := s.Tools(ToolSetLatest)
	if len(toolGroups) != 25 || len(tools) != 25 {
		t.Fatalf("groups = %d, tools = %d, want 25", len(toolGroups), len(tools))
	}
	var changes []string
	for _, tl := range tools {
		g := Group(tl.Name)
		if g == "" {
			t.Errorf("%s has no group", tl.Name)
		}
		if g == "changes" {
			changes = append(changes, tl.Name)
			if tl.Annotations.ReadOnlyHint {
				t.Errorf("%s is read-only, but in the group changes", tl.Name)
			}
		} else if !tl.Annotations.ReadOnlyHint {
			t.Errorf("%s changes data, but is in the group %s", tl.Name, g)
		}
	}
	if len(changes) != len(writeToolNames) || !slices.Equal(slices.Sorted(slices.Values(changes)), slices.Sorted(slices.Values(writeToolNames))) {
		t.Errorf("changes group = %v", changes)
	}
	if Group("nope") != "" {
		t.Error("unknown tool has a group")
	}
}
