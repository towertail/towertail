package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/assistant"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
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

func (f *fakeCallLog) all() []clickhouse.AICall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]clickhouse.AICall(nil), f.rows...)
}

func (f *fakeCallLog) only(t *testing.T) clickhouse.AICall {
	t.Helper()
	rows := f.all()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(rows), rows)
	}
	return rows[0]
}

// fakeAIReader serves fixed rows, newest first, and keeps the last query.
type fakeAIReader struct {
	rows    []clickhouse.AICall
	gotQ    clickhouse.AICallQuery
	gotUser *uuid.UUID
}

func (f *fakeAIReader) visible(q clickhouse.AICallQuery) []clickhouse.AICall {
	var out []clickhouse.AICall
	for _, r := range f.rows {
		if r.OrgID != q.OrgID || (q.UserID != nil && (r.UserID == nil || *r.UserID != *q.UserID)) {
			continue
		}
		if !q.BeforeTS.IsZero() && !(r.TS.Before(q.BeforeTS) || (r.TS.Equal(q.BeforeTS) && r.CallID.String() < q.BeforeID.String())) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (f *fakeAIReader) AICalls(_ context.Context, q clickhouse.AICallQuery) ([]clickhouse.AICall, error) {
	f.gotQ = q
	rows := f.visible(q)
	if q.Limit > 0 && len(rows) > q.Limit {
		rows = rows[:q.Limit]
	}
	return rows, nil
}

func (f *fakeAIReader) AICall(_ context.Context, org, id uuid.UUID) (clickhouse.AICall, error) {
	for _, r := range f.rows {
		if r.OrgID == org && r.CallID == id {
			return r, nil
		}
	}
	return clickhouse.AICall{}, clickhouse.ErrNoAICall
}

func (f *fakeAIReader) AICallStats(_ context.Context, q clickhouse.AICallQuery) (clickhouse.AICallStats, error) {
	f.gotQ = q
	return clickhouse.AICallStats{Calls: len(f.visible(q)), Clients: []string{"Claude"}}, nil
}

func (f *fakeAIReader) AICallUsage(_ context.Context, org uuid.UUID, user *uuid.UUID, _ time.Time) ([]clickhouse.AICallUsage, error) {
	f.gotUser = user
	var out []clickhouse.AICallUsage
	for _, r := range f.visible(clickhouse.AICallQuery{OrgID: org, UserID: user}) {
		u := clickhouse.AICallUsage{Calls: 1, LastAt: r.TS}
		if r.GrantID != nil {
			u.GrantID = *r.GrantID
		} else {
			u.TokenID = r.TokenID
		}
		out = append(out, u)
	}
	return out, nil
}

func TestAICallCursor(t *testing.T) {
	ts, id := time.Date(2026, 11, 20, 12, 0, 0, 123_000_000, time.UTC), uuid.New()
	gotTS, gotID, err := decodeCursor(encodeCursor(ts, id))
	if err != nil || !gotTS.Equal(ts) || gotID != id {
		t.Fatalf("decode = %v, %v, %v", gotTS, gotID, err)
	}
	for _, bad := range []string{"!!", "", "bm9kb3Q", "MTIz", "YWJjLmRlZg"} {
		if _, _, err := decodeCursor(bad); err == nil {
			t.Errorf("cursor %q: no error", bad)
		}
	}
}

func aiRows(org uuid.UUID, a, b *uuid.UUID) []clickhouse.AICall {
	now := time.Now().UTC().Truncate(time.Millisecond)
	grant := uuid.New()
	mk := func(i int, user *uuid.UUID, tool, change string) clickhouse.AICall {
		return clickhouse.AICall{
			TS: now.Add(-time.Duration(i) * time.Minute), CallID: uuid.New(), OrgID: org, UserID: user, TokenID: uuid.New(),
			GrantID: &grant, Via: "oauth", Client: "Claude", Tool: tool, Input: `{"x":1}`, Status: "ok", DurationMs: 5, Rows: 3, Bytes: 40,
			Change: change,
		}
	}
	return []clickhouse.AICall{
		mk(1, a, "list_hosts", ""), mk(2, b, "alerts", ""), mk(3, a, "ack_alert", `{"targets":[{"kind":"alert","id":"x"}]}`), mk(4, b, "whoami", ""),
	}
}

func decodeBody[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return v
}

func TestAICallRoutesVisibility(t *testing.T) {
	e := newKeyEnv(t, config.HTTPConfig{})
	admin, adminID := e.login(t, store.RoleAdmin)
	member, memberID := e.login(t, store.RoleMember)
	_, otherID := e.login(t, store.RoleMember)
	fake := &fakeAIReader{rows: aiRows(store.ZeroOrg, &memberID, &otherID)}
	e.srv.aiCalls = fake
	_ = adminID

	list := func(token, query string) wire.AICallPage {
		t.Helper()
		rec := e.do(http.MethodGet, "/v1/ai/calls"+query, token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list %s: %d %s", query, rec.Code, rec.Body)
		}
		return decodeBody[wire.AICallPage](t, rec.Body.Bytes())
	}
	if page := list(admin, ""); len(page.Calls) != 4 || page.Next != "" {
		t.Fatalf("admin sees %d calls, next %q", len(page.Calls), page.Next)
	}
	page := list(member, "")
	if len(page.Calls) != 2 || fake.gotQ.UserID == nil || *fake.gotQ.UserID != memberID {
		t.Fatalf("member sees %d calls, query user %v", len(page.Calls), fake.gotQ.UserID)
	}
	for _, c := range page.Calls {
		if c.UserID == nil || *c.UserID != memberID {
			t.Errorf("member sees a call of %v", c.UserID)
		}
	}
	// Defaults and filters reach the reader.
	list(admin, "?range=24h&client=Claude&tool=alerts&status=errors")
	q := fake.gotQ
	if q.Client != "Claude" || q.Tool != "alerts" || q.Filter != clickhouse.AICallErrors || q.To.Sub(q.From) != 24*time.Hour || q.Limit != 51 {
		t.Fatalf("query = %+v", q)
	}
	list(admin, "")
	if q := fake.gotQ; q.To.Sub(q.From) != 7*24*time.Hour {
		t.Fatalf("default range = %v", q.To.Sub(q.From))
	}

	// Pages: limit 3 of 4 rows gives a cursor, and the cursor gives the rest.
	first := list(admin, "?limit=3")
	if len(first.Calls) != 3 || first.Next == "" {
		t.Fatalf("first page = %d calls, next %q", len(first.Calls), first.Next)
	}
	rest := list(admin, "?limit=3&cursor="+first.Next)
	if len(rest.Calls) != 1 || rest.Next != "" || rest.Calls[0].ID == first.Calls[0].ID {
		t.Fatalf("second page = %+v", rest)
	}

	// Bad parameters give 400.
	for _, qs := range []string{"?range=1y", "?status=bad", "?limit=0", "?limit=101", "?limit=x", "?cursor=!!"} {
		if rec := e.do(http.MethodGet, "/v1/ai/calls"+qs, admin, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", qs, rec.Code)
		}
	}

	// Detail: the owner of the call and an admin read it. Another member gets 404.
	mine, theirs := fake.rows[0], fake.rows[1]
	get := func(token string, id uuid.UUID) *httptest.ResponseRecorder {
		return e.do(http.MethodGet, "/v1/ai/calls/"+id.String(), token, nil)
	}
	if rec := get(member, mine.CallID); rec.Code != http.StatusOK {
		t.Fatalf("own call: %d", rec.Code)
	}
	if rec := get(member, theirs.CallID); rec.Code != http.StatusNotFound {
		t.Fatalf("call of another user: %d, want 404", rec.Code)
	}
	rec := get(admin, fake.rows[2].CallID)
	d := decodeBody[wire.AICallDetail](t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || d.Tool != "ack_alert" || !strings.Contains(string(d.Change), `"targets"`) || d.Input != `{"x":1}` {
		t.Fatalf("admin detail: %d %+v", rec.Code, d)
	}
	for _, id := range []string{uuid.NewString(), "nope"} {
		if rec := e.do(http.MethodGet, "/v1/ai/calls/"+id, admin, nil); rec.Code != http.StatusNotFound {
			t.Errorf("call %s: %d, want 404", id, rec.Code)
		}
	}

	// Stats and usage follow the same rule.
	stats := decodeBody[wire.AICallStats](t, e.do(http.MethodGet, "/v1/ai/calls/stats", member, nil).Body.Bytes())
	if stats.Calls != 2 || len(stats.Clients) != 1 || stats.Tools == nil {
		t.Fatalf("member stats = %+v", stats)
	}
	if stats := decodeBody[wire.AICallStats](t, e.do(http.MethodGet, "/v1/ai/calls/stats", admin, nil).Body.Bytes()); stats.Calls != 4 {
		t.Fatalf("admin stats = %+v", stats)
	}
	usage := decodeBody[[]wire.AIUsage](t, e.do(http.MethodGet, "/v1/ai/usage", member, nil).Body.Bytes())
	if len(usage) != 2 || usage[0].GrantID == nil || usage[0].TokenID != nil || fake.gotUser == nil || *fake.gotUser != memberID {
		t.Fatalf("member usage = %+v", usage)
	}
	if usage := decodeBody[[]wire.AIUsage](t, e.do(http.MethodGet, "/v1/ai/usage?days=30", admin, nil).Body.Bytes()); len(usage) != 4 || fake.gotUser != nil {
		t.Fatalf("admin usage = %+v", usage)
	}
	for _, qs := range []string{"?days=0", "?days=31", "?days=x"} {
		if rec := e.do(http.MethodGet, "/v1/ai/usage"+qs, admin, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("usage %s: %d, want 400", qs, rec.Code)
		}
	}

	// An API key gets 403 on every route. No token gets 401.
	key := e.create(t, member, "ci").Token
	for _, p := range []string{"/v1/ai/calls", "/v1/ai/calls/stats", "/v1/ai/calls/" + uuid.NewString(), "/v1/ai/usage", "/v1/ai/tools"} {
		if rec := e.do(http.MethodGet, p, key, nil); rec.Code != http.StatusForbidden {
			t.Errorf("api key on %s: %d, want 403", p, rec.Code)
		}
		if rec := e.do(http.MethodGet, p, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("no token on %s: %d, want 401", p, rec.Code)
		}
	}
}

func TestAICallRoutesNoClickHouse(t *testing.T) {
	e := newMCPEnv(t, config.HTTPConfig{}, true)
	user, _ := e.login(t, store.RoleOwner)
	for _, p := range []string{"/v1/ai/calls", "/v1/ai/calls/stats", "/v1/ai/calls/" + uuid.NewString(), "/v1/ai/usage"} {
		if rec := e.do(http.MethodGet, p, user, nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"unavailable"`) {
			t.Errorf("%s: %d %s, want 503 unavailable", p, rec.Code, rec.Body)
		}
	}
	if rec := e.do(http.MethodGet, "/v1/ai/tools", user, nil); rec.Code != http.StatusOK {
		t.Errorf("tools: %d, want 200", rec.Code)
	}
}

func TestAIToolsRoute(t *testing.T) {
	e := newMCPEnv(t, config.HTTPConfig{}, true)
	viewer, _ := e.login(t, store.RoleViewer)
	rec := e.do(http.MethodGet, "/v1/ai/tools", viewer, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools: %d %s", rec.Code, rec.Body)
	}
	tools := decodeBody[[]wire.AITool](t, rec.Body.Bytes())
	if len(tools) != 25 {
		t.Fatalf("tools = %d, want 25", len(tools))
	}
	by := map[string]wire.AITool{}
	for _, tl := range tools {
		if tl.Title == "" || tl.Group == "" || tl.Description == "" || tl.Destructive {
			t.Errorf("tool = %+v", tl)
		}
		by[tl.Name] = tl
	}
	if g := by["list_hosts"]; g.Group != "fleet" || !g.ReadOnly {
		t.Errorf("list_hosts = %+v", g)
	}
	if g := by["set_thresholds"]; g.Group != "changes" || g.ReadOnly {
		t.Errorf("set_thresholds = %+v", g)
	}

	off := newMCPEnv(t, config.HTTPConfig{}, false)
	viewer, _ = off.login(t, store.RoleViewer)
	if rec := off.do(http.MethodGet, "/v1/ai/tools", viewer, nil); rec.Code != http.StatusNotFound {
		t.Errorf("tools with MCP off: %d, want 404", rec.Code)
	}
}

func TestMCPHTTPRecordsCall(t *testing.T) {
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := &fakeCallLog{}
	issuer, roles := auth.NewIssuer(st), userRoles{}
	srv := New(Deps{
		Config: &config.Config{MCP: config.MCPConfig{Enabled: true}}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store: st, Issuer: issuer, Roles: roles, Calls: log,
	})
	e := &keyEnv{testEnv: &testEnv{t: t, srv: srv, st: st, issuer: issuer}, roles: roles}
	user, userID := e.login(t, store.RoleViewer)
	key := e.create(t, user, "my key").Token

	for _, path := range []string{"/mcp", "/mcp/v1"} {
		rec := e.mcpPostTo(t, path, http.MethodPost, key, mcpListHosts)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	rows := log.all()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	r := rows[0]
	if r.Via != "api_key" || r.Client != "my key" || r.Tool != "list_hosts" || r.Status != "error" || r.UserID == nil || *r.UserID != userID {
		t.Fatalf("row = %+v", r)
	}
	// tools/list is not a tool call.
	e.mcpPost(t, http.MethodPost, key, mcpToolsList)
	if n := len(log.all()); n != 2 {
		t.Fatalf("rows after tools/list = %d, want 2", n)
	}
}

// proposeTool saves a pending proposal with a tool, an input and rows.
func (e *assistantEnv) proposeTool(chat uuid.UUID, tool, kind string, minRole store.Role, input string) uuid.UUID {
	e.t.Helper()
	p := assistant.Proposal{
		ID: uuid.New(), OrgID: store.ZeroOrg, ChatID: chat, Status: assistant.ProposalPending, CreatedAt: time.Now(),
		Change: assistant.Change{
			Tool: tool, Kind: kind, MinRole: minRole, Backup: kind == "rules", Input: json.RawMessage(input),
			Rows: []assistant.ChangeRow{
				{ID: "r1", Target: "web-1", Field: "cpu.warn", Old: "0.8", New: "0.7", Checked: true},
				{ID: "r2", Target: "db-1", Field: "cpu.warn", Old: "0.8", New: "0.7", Checked: true},
			},
		},
	}
	if kind == "alerts" {
		p.Rows = p.Rows[:1]
		p.Rows[0].Target, p.Rows[0].Field = "alert "+uuid.NewString(), "ack"
	}
	if err := e.ms.SaveProposal(context.Background(), p); err != nil {
		e.t.Fatal(err)
	}
	return p.ID
}

func TestRecordProposal(t *testing.T) {
	e := newAssistantEnv(t, aiConfig(), nil).start()
	log := &fakeCallLog{}
	e.srv.calls = log
	e.setSettings(func(s *store.ServerSettings) { s.MCP.AIChanges = store.AIChangesAlertsAndRules })
	member, memberID := e.login(store.RoleMember)
	admin, _ := e.login(store.RoleAdmin)
	viewer, _ := e.login(store.RoleViewer)
	post := func(token string, id uuid.UUID, verb, body string) int {
		code, _ := e.do(http.MethodPost, "/v1/assistant/proposals/"+id.String()+"/"+verb, token, body)
		return code
	}

	// Apply of an alert action, then undo with undo:true.
	ack := e.proposeTool(e.newChat(member), "ack_alert", "alerts", store.RoleMember, `{"alert_id":"a1"}`)
	if code := post(member, ack, "apply", `{"rows":["r1"]}`); code != http.StatusOK {
		t.Fatalf("apply: %d", code)
	}
	r := log.only(t)
	if r.Via != "assistant" || r.Client != "assistant" || r.Tool != "ack_alert" || r.Status != "ok" || r.UserID == nil || *r.UserID != memberID ||
		r.TokenID == uuid.Nil || r.TS.IsZero() || r.CallID == uuid.Nil {
		t.Fatalf("apply row = %+v", r)
	}
	var in map[string]any
	if err := json.Unmarshal([]byte(r.Input), &in); err != nil || in["alert_id"] != "a1" || len(in["rows"].([]any)) != 1 {
		t.Fatalf("input = %s, %v", r.Input, err)
	}
	var ch struct {
		Targets  []map[string]any `json:"targets"`
		Fields   []map[string]any `json:"fields"`
		BackupID string           `json:"backup_id"`
	}
	if err := json.Unmarshal([]byte(r.Change), &ch); err != nil || len(ch.Targets) != 1 || ch.Targets[0]["kind"] != "alert" || len(ch.Fields) != 1 {
		t.Fatalf("change = %s, %v", r.Change, err)
	}
	if code := post(member, ack, "undo", ""); code != http.StatusOK {
		t.Fatalf("undo: %d", code)
	}
	r = log.all()[1]
	if err := json.Unmarshal([]byte(r.Input), &in); err != nil || r.Tool != "ack_alert" || in["undo"] != true || r.Status != "ok" {
		t.Fatalf("undo row = %+v, %v", r, err)
	}

	// Apply of a rules change has the backup in the change. Its undo is undo_rules_change.
	log.rows = nil
	rules := e.proposeTool(e.newChat(admin), "set_thresholds", "rules", store.RoleAdmin, `{"metric":"cpu"}`)
	if code := post(admin, rules, "apply", `{"rows":["r2"]}`); code != http.StatusOK {
		t.Fatalf("apply rules: %d", code)
	}
	backup := e.writer.lastBackup()
	r = log.only(t)
	if err := json.Unmarshal([]byte(r.Change), &ch); err != nil || ch.BackupID != backup.String() || len(ch.Fields) != 1 || ch.Fields[0]["target"] != "db-1" ||
		ch.Fields[0]["before"] != "0.8" || r.Tool != "set_thresholds" {
		t.Fatalf("rules change = %s, %v (backup %s)", r.Change, err, backup)
	}
	if code := post(admin, rules, "undo", ""); code != http.StatusOK {
		t.Fatalf("undo rules: %d", code)
	}
	r = log.all()[1]
	if err := json.Unmarshal([]byte(r.Input), &in); err != nil || r.Tool != "undo_rules_change" || in["backup_id"] != backup.String() {
		t.Fatalf("undo rules row = %+v, %v", r, err)
	}
	if err := json.Unmarshal([]byte(r.Change), &ch); err != nil || ch.Fields[0]["before"] != "0.7" {
		t.Fatalf("undo change = %s, %v", r.Change, err)
	}

	// A role error is denied. The row has the tool, though the apply failed.
	log.rows = nil
	denied := e.proposeTool(e.newChat(viewer), "set_thresholds", "rules", store.RoleAdmin, `{"metric":"cpu"}`)
	if code := post(viewer, denied, "apply", `{"rows":["r1"]}`); code != http.StatusForbidden {
		t.Fatalf("viewer apply: %d", code)
	}
	if r = log.only(t); r.Status != "denied" || r.Tool != "set_thresholds" || r.Error == "" || r.Via != "assistant" {
		t.Fatalf("denied row = %+v", r)
	}

	// Another failure is an error.
	log.rows = nil
	if code := post(admin, rules, "apply", `{"rows":["r1"]}`); code != http.StatusConflict {
		t.Fatalf("second apply: %d", code)
	}
	if r = log.only(t); r.Status != "error" || r.Tool != "set_thresholds" {
		t.Fatalf("error row = %+v", r)
	}

	// An unknown proposal gives no row.
	log.rows = nil
	if code := post(admin, uuid.New(), "apply", `{"rows":["r1"]}`); code != http.StatusNotFound || len(log.all()) != 0 {
		t.Fatalf("unknown proposal: %d, rows %d", code, len(log.all()))
	}
}
