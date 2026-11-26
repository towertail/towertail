package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"iter"
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
	"github.com/towertail/towertail/server/internal/mcp"
	"github.com/towertail/towertail/server/store"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// fakeAIModel answers "Hello" after the delay. cancelled closes when the
// context ends first.
type fakeAIModel struct {
	delay     time.Duration
	started   chan struct{}
	cancelled chan struct{}
	once      sync.Once
}

func (m *fakeAIModel) Stream(ctx context.Context, _ assistant.Request) iter.Seq2[assistant.Part, error] {
	return func(yield func(assistant.Part, error) bool) {
		if m.started != nil {
			m.once.Do(func() { close(m.started) })
		}
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			if m.cancelled != nil {
				close(m.cancelled)
			}
			return
		}
		if !yield(assistant.Part{Kind: assistant.PartText, Text: "Hello"}, nil) {
			return
		}
		yield(assistant.Part{Kind: assistant.PartStop, Stop: assistant.StopEnd, Usage: &assistant.Usage{Input: 1, Output: 1}}, nil)
	}
}

type noTools struct{}

func (noTools) ToolSpecs() []mcp.ToolSpec { return nil }
func (noTools) Call(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

type aiWriter struct {
	mu      sync.Mutex
	applied [][]string
	undone  int
	backups []uuid.UUID
}

func (w *aiWriter) lastBackup() uuid.UUID {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.backups[len(w.backups)-1]
}

func (w *aiWriter) Plan(context.Context, assistant.Actor, string, json.RawMessage) (assistant.Change, error) {
	return assistant.Change{}, nil
}

func (w *aiWriter) Apply(_ context.Context, _ assistant.Actor, _ assistant.Change, rows []string) (assistant.Applied, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.applied = append(w.applied, rows)
	w.backups = append(w.backups, uuid.New())
	return assistant.Applied{Rows: rows, BackupID: w.backups[len(w.backups)-1]}, nil
}

func (w *aiWriter) Undo(context.Context, assistant.Actor, assistant.Change, assistant.Applied) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.undone++
	return nil
}

type fixedPlan struct {
	plan  string
	seats int
}

func (p fixedPlan) AssistantPlan(context.Context, uuid.UUID) (string, int, error) {
	return p.plan, p.seats, nil
}

type assistantEnv struct {
	t      *testing.T
	ts     *httptest.Server
	srv    *Server
	st     *store.BoltStore
	ms     *assistant.MemoryStore
	model  *fakeAIModel
	writer *aiWriter
	logs   *lockedBuf
	issuer *auth.TokenIssuer
	roles  userRoles
}

func aiConfig() config.AssistantConfig {
	return config.AssistantConfig{Model: "claude-sonnet-5-5", APIKey: "test"}
}

// newAssistantEnv is a live server with the assistant on, a fake model and
// a fake writer.
func newAssistantEnv(t *testing.T, ac config.AssistantConfig, plans AssistantPlans) *assistantEnv {
	t.Helper()
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logs := &lockedBuf{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	e := &assistantEnv{
		t: t, st: st, logs: logs, ms: assistant.NewMemoryStore(), model: &fakeAIModel{}, writer: &aiWriter{},
		issuer: auth.NewIssuer(st), roles: userRoles{},
	}
	e.srv = New(Deps{Config: &config.Config{}, Log: log, Store: st, Issuer: e.issuer, Roles: e.roles})
	var ps assistant.PlanSource
	if plans != nil {
		ps = plans
	}
	e.srv.assistant = assistant.NewService(ac, e.model, noTools{}, e.writer, e.ms, ps, settingsSource{st}, log)
	e.srv.ssePing = time.Hour
	e.ts = httptest.NewUnstartedServer(e.srv.Handler())
	return e
}

func (e *assistantEnv) start() *assistantEnv {
	e.ts.Start()
	e.t.Cleanup(e.ts.Close)
	return e
}

func (e *assistantEnv) login(role store.Role) (string, uuid.UUID) {
	e.t.Helper()
	u := uuid.New()
	e.roles[u] = role
	raw, _, err := e.issuer.Issue(context.Background(), store.ZeroOrg, store.TokenKindUser, "login", nil, &u)
	if err != nil {
		e.t.Fatal(err)
	}
	return raw, u
}

func (e *assistantEnv) req(ctx context.Context, method, path, token, body string, hdr ...string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, e.ts.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *assistantEnv) do(method, path, token, body string) (int, []byte) {
	e.t.Helper()
	res := e.req(context.Background(), method, path, token, body)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func (e *assistantEnv) newChat(token string) uuid.UUID {
	e.t.Helper()
	code, b := e.do(http.MethodPost, "/v1/assistant/chats", token, `{"title":"t"}`)
	if code != http.StatusCreated {
		e.t.Fatalf("create chat: %d %s", code, b)
	}
	var c assistant.Chat
	if err := json.Unmarshal(b, &c); err != nil {
		e.t.Fatal(err)
	}
	return c.ID
}

func (e *assistantEnv) ask(token string, chat uuid.UUID) (int, string) {
	e.t.Helper()
	code, b := e.do(http.MethodPost, "/v1/assistant/chats/"+chat.String()+"/messages", token, `{"text":"why?"}`)
	return code, string(b)
}

// propose saves a pending proposal in the chat.
func (e *assistantEnv) propose(chat uuid.UUID, kind string, minRole store.Role) uuid.UUID {
	e.t.Helper()
	p := assistant.Proposal{
		ID: uuid.New(), OrgID: store.ZeroOrg, ChatID: chat, Status: assistant.ProposalPending, CreatedAt: time.Now(),
		Change: assistant.Change{
			Tool: "t", Kind: kind, MinRole: minRole, Backup: kind == "rules",
			Rows: []assistant.ChangeRow{{ID: "r1", Checked: true}, {ID: "r2", Checked: true}},
		},
	}
	if err := e.ms.SaveProposal(context.Background(), p); err != nil {
		e.t.Fatal(err)
	}
	return p.ID
}

func (e *assistantEnv) setSettings(f func(*store.ServerSettings)) {
	e.t.Helper()
	s, err := e.st.GetSettings(context.Background(), store.ZeroOrg)
	if err != nil {
		e.t.Fatal(err)
	}
	f(&s)
	if err := e.st.PutSettings(context.Background(), store.ZeroOrg, s); err != nil {
		e.t.Fatal(err)
	}
}

type errBody struct {
	Error struct {
		Code    string `json:"code"`
		ResetAt string `json:"resetAt"`
	} `json:"error"`
}

func errCode(t *testing.T, b []byte) errBody {
	t.Helper()
	var e errBody
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatalf("error body %s: %v", b, err)
	}
	return e
}

func TestAssistantTokens(t *testing.T) {
	e := newAssistantEnv(t, aiConfig(), nil).start()
	user, _ := e.login(store.RoleViewer)
	key := (&keyEnv{testEnv: &testEnv{t: t, srv: e.srv, st: e.st, issuer: e.issuer}, roles: e.roles}).create(t, user, "k").Token
	if code, _ := e.do(http.MethodPost, "/v1/assistant/chats", key, `{}`); code != http.StatusForbidden && code != http.StatusUnauthorized {
		t.Fatalf("api key: %d, want 401 or 403", code)
	}
	if code, _ := e.do(http.MethodPost, "/v1/assistant/chats", "", `{}`); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	chat := e.newChat(user)
	code, body := e.ask(user, chat)
	if code != http.StatusOK || !strings.Contains(body, "event: done") {
		t.Fatalf("viewer ask: %d %s", code, body)
	}
}

func TestAssistantChats(t *testing.T) {
	e := newAssistantEnv(t, aiConfig(), nil).start()
	user, _ := e.login(store.RoleViewer)
	other, _ := e.login(store.RoleViewer)
	chat := e.newChat(user)
	code, b := e.do(http.MethodGet, "/v1/assistant/chats", user, "")
	if code != http.StatusOK || !strings.Contains(string(b), chat.String()) {
		t.Fatalf("list: %d %s", code, b)
	}
	if code, b := e.do(http.MethodGet, "/v1/assistant/chats", other, ""); code != http.StatusOK || !strings.Contains(string(b), `"chats":[]`) {
		t.Fatalf("other list: %d %s", code, b)
	}
	if code, _ := e.do(http.MethodGet, "/v1/assistant/chats/"+chat.String(), other, ""); code != http.StatusNotFound {
		t.Fatalf("other get: %d", code)
	}
	if code, b := e.do(http.MethodGet, "/v1/assistant/chats/"+chat.String(), user, ""); code != http.StatusOK || !strings.Contains(string(b), `"messages"`) {
		t.Fatalf("get: %d %s", code, b)
	}
	if code, _ := e.do(http.MethodDelete, "/v1/assistant/chats/"+chat.String(), user, ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := e.do(http.MethodGet, "/v1/assistant/chats/"+chat.String(), user, ""); code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", code)
	}
}

func TestAssistantGetChatProposals(t *testing.T) {
	e := newAssistantEnv(t, aiConfig(), nil).start()
	member, _ := e.login(store.RoleMember)
	chat := e.newChat(member)
	get := func() struct {
		Proposals []struct {
			ID, Status, BackupID string
		} `json:"proposals"`
	} {
		code, b := e.do(http.MethodGet, "/v1/assistant/chats/"+chat.String(), member, "")
		if code != http.StatusOK || !strings.Contains(string(b), `"proposals":[`) {
			t.Fatalf("get: %d %s", code, b)
		}
		var out struct {
			Proposals []struct {
				ID, Status, BackupID string
			} `json:"proposals"`
		}
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if n := len(get().Proposals); n != 0 {
		t.Fatalf("proposals = %d, want 0", n)
	}

	id := e.propose(chat, "alerts", store.RoleMember)
	if got := get().Proposals; len(got) != 1 || got[0].ID != id.String() || got[0].Status != "pending" {
		t.Fatalf("pending: %+v", got)
	}
	if code, b := e.do(http.MethodPost, "/v1/assistant/proposals/"+id.String()+"/apply", member, `{"rows":["r1"]}`); code != http.StatusOK {
		t.Fatalf("apply: %d %s", code, b)
	}
	if got := get().Proposals; len(got) != 1 || got[0].Status != "applied" {
		t.Fatalf("applied: %+v", got)
	}
}

func TestAssistantApplyRights(t *testing.T) {
	e := newAssistantEnv(t, aiConfig(), nil).start()
	viewer, _ := e.login(store.RoleViewer)
	member, _ := e.login(store.RoleMember)
	admin, _ := e.login(store.RoleAdmin)
	apply := func(token string, id uuid.UUID, rows string) (int, errBody) {
		code, b := e.do(http.MethodPost, "/v1/assistant/proposals/"+id.String()+"/apply", token, `{"rows":`+rows+`}`)
		if code == http.StatusOK {
			return code, errBody{}
		}
		return code, errCode(t, b)
	}

	rules := e.propose(e.newChat(viewer), "rules", store.RoleAdmin)
	if code, eb := apply(viewer, rules, `["r1"]`); code != http.StatusForbidden || eb.Error.Code != "forbidden" {
		t.Fatalf("viewer rules: %d %+v", code, eb)
	}

	alerts := e.propose(e.newChat(member), "alerts", store.RoleMember)
	if code, eb := apply(member, alerts, `["r1","r2"]`); code != http.StatusOK {
		t.Fatalf("member alerts: %d %+v", code, eb)
	}
	if len(e.writer.applied) != 1 || strings.Join(e.writer.applied[0], ",") != "r1,r2" {
		t.Fatalf("writer rows = %v", e.writer.applied)
	}
	if !strings.Contains(e.logs.String(), "assistant.apply") {
		t.Fatalf("no audit line in %s", e.logs.String())
	}
	if code, eb := apply(member, alerts, `["r1"]`); code != http.StatusConflict {
		t.Fatalf("second apply: %d %+v", code, eb)
	}
	code, b := e.do(http.MethodPost, "/v1/assistant/proposals/"+alerts.String()+"/undo", member, "")
	if code != http.StatusOK || e.writer.undone != 1 || !strings.Contains(e.logs.String(), "assistant.undo") {
		t.Fatalf("undo: %d %s undone=%d", code, b, e.writer.undone)
	}

	// The default of Changes by AI is alerts: an admin cannot apply rules.
	adminRules := e.propose(e.newChat(admin), "rules", store.RoleAdmin)
	if code, eb := apply(admin, adminRules, `["r1"]`); code != http.StatusForbidden || eb.Error.Code != "ai_changes_off" {
		t.Fatalf("aiChanges alerts: %d %+v", code, eb)
	}
	e.setSettings(func(s *store.ServerSettings) { s.MCP.AIChanges = store.AIChangesAlertsAndRules })
	if code, eb := apply(admin, adminRules, `["r1"]`); code != http.StatusOK {
		t.Fatalf("aiChanges alerts_and_rules: %d %+v", code, eb)
	}
	if code, eb := apply(admin, adminRules, `[]`); code == http.StatusOK {
		t.Fatalf("empty rows: %d %+v", code, eb)
	}
}

func TestAssistantOffSwitch(t *testing.T) {
	e := newAssistantEnv(t, aiConfig(), nil).start()
	member, _ := e.login(store.RoleMember)
	chat := e.newChat(member)
	prop := e.propose(chat, "alerts", store.RoleMember)
	e.setSettings(func(s *store.ServerSettings) { s.AssistantOff = true })

	code, body := e.ask(member, chat)
	if code != http.StatusForbidden || errCode(t, []byte(body)).Error.Code != "assistant_off" {
		t.Fatalf("ask: %d %s", code, body)
	}
	code, b := e.do(http.MethodPost, "/v1/assistant/proposals/"+prop.String()+"/apply", member, `{"rows":["r1"]}`)
	if code != http.StatusForbidden || errCode(t, b).Error.Code != "assistant_off" {
		t.Fatalf("apply: %d %s", code, b)
	}
	if code, _ := e.do(http.MethodGet, "/v1/assistant/chats", member, ""); code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	code, b = e.do(http.MethodGet, "/v1/assistant/usage", member, "")
	if code != http.StatusOK || !strings.Contains(string(b), `"off":true`) {
		t.Fatalf("usage: %d %s", code, b)
	}
}

func TestAssistantUnavailable(t *testing.T) {
	for name, cfg := range map[string]config.Config{
		"config off": {},
		// A Bolt server has no assistant store.
		"no store": {Assistant: aiConfig()},
	} {
		e := newKeyEnvConfig(t, &cfg)
		user, _ := e.login(t, store.RoleOwner)
		chats := e.do(http.MethodPost, "/v1/assistant/chats", user, []byte(`{}`))
		if chats.Code != http.StatusServiceUnavailable || errCode(t, chats.Body.Bytes()).Error.Code != "assistant_unavailable" {
			t.Errorf("%s: chats %d %s", name, chats.Code, chats.Body)
		}
		msg := e.do(http.MethodPost, "/v1/assistant/chats/"+uuid.NewString()+"/messages", user, []byte(`{"text":"x"}`))
		if msg.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: messages %d", name, msg.Code)
		}
		usage := e.do(http.MethodGet, "/v1/assistant/usage", user, nil)
		if usage.Code != http.StatusOK || strings.TrimSpace(usage.Body.String()) != `{"enabled":false}` {
			t.Errorf("%s: usage %d %s", name, usage.Code, usage.Body)
		}
	}
}

func TestAssistantStream(t *testing.T) {
	e := newAssistantEnv(t, aiConfig(), nil)
	e.model.delay = 150 * time.Millisecond
	e.srv.ssePing = 20 * time.Millisecond
	e.start()
	user, _ := e.login(store.RoleViewer)
	chat := e.newChat(user)
	res := e.req(context.Background(), http.MethodPost, "/v1/assistant/chats/"+chat.String()+"/messages", user, `{"text":"why?"}`, "Accept-Encoding", "gzip")
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	body := string(b)
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content type %q", ct)
	}
	if res.Header.Get("Content-Encoding") != "" {
		t.Errorf("content encoding %q", res.Header.Get("Content-Encoding"))
	}
	if res.Header.Get("X-Accel-Buffering") != "no" || res.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("headers %v", res.Header)
	}
	if !strings.Contains(body, ": ping\n\n") {
		t.Errorf("no keep-alive in %q", body)
	}
	prev := -1
	for _, ev := range []string{"event: text\ndata: {\"delta\":\"Hello\"}", "event: usage", "event: done"} {
		i := strings.Index(body, ev)
		if i < 0 || i < prev {
			t.Fatalf("event %q out of order in %q", ev, body)
		}
		prev = i
	}
}

func TestAssistantStreamOutlivesWriteTimeout(t *testing.T) {
	e := newAssistantEnv(t, aiConfig(), nil)
	e.model.delay = 2 * time.Second
	e.ts.Config.WriteTimeout = time.Second
	e.start()
	user, _ := e.login(store.RoleViewer)
	code, body := e.ask(user, e.newChat(user))
	if code != http.StatusOK || !strings.Contains(body, "event: done") {
		t.Fatalf("%d %q", code, body)
	}
}

func TestAssistantAbortCancelsModel(t *testing.T) {
	e := newAssistantEnv(t, aiConfig(), nil)
	e.model.delay = time.Minute
	e.model.started, e.model.cancelled = make(chan struct{}), make(chan struct{})
	e.start()
	user, _ := e.login(store.RoleViewer)
	chat := e.newChat(user)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.ts.URL+"/v1/assistant/chats/"+chat.String()+"/messages", strings.NewReader(`{"text":"why?"}`))
		req.Header.Set("Authorization", "Bearer "+user)
		if res, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
	}()
	select {
	case <-e.model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the model did not start")
	}
	cancel()
	<-done
	select {
	case <-e.model.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the model context did not end")
	}
}

func TestAssistantLimits(t *testing.T) {
	ac := aiConfig()
	ac.Limits = config.AssistantLimits{
		Default: config.AssistantPlanLimits{QuestionsPerDay: 1},
		Plans:   map[string]config.AssistantPlanLimits{"team": {QuestionsPerDay: 10, QuestionsPerSeatMonth: 1}},
	}
	t.Run("day", func(t *testing.T) {
		e := newAssistantEnv(t, ac, nil).start()
		user, _ := e.login(store.RoleViewer)
		chat := e.newChat(user)
		if code, body := e.ask(user, chat); code != http.StatusOK {
			t.Fatalf("first: %d %s", code, body)
		}
		res := e.req(context.Background(), http.MethodPost, "/v1/assistant/chats/"+chat.String()+"/messages", user, `{"text":"again"}`)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		eb := errCode(t, b)
		if res.StatusCode != http.StatusTooManyRequests || eb.Error.Code != "daily_limit" {
			t.Fatalf("second: %d %s", res.StatusCode, b)
		}
		if res.Header.Get("Retry-After") == "" {
			t.Error("no Retry-After")
		}
		reset, err := time.Parse(time.RFC3339, eb.Error.ResetAt)
		if err != nil || reset.Hour() != 0 || reset.Minute() != 0 || !reset.After(time.Now()) {
			t.Errorf("resetAt %q", eb.Error.ResetAt)
		}
	})
	t.Run("month pool", func(t *testing.T) {
		e := newAssistantEnv(t, ac, fixedPlan{plan: "team", seats: 2}).start()
		user, _ := e.login(store.RoleViewer)
		chat := e.newChat(user)
		for i := range 2 {
			if code, body := e.ask(user, chat); code != http.StatusOK {
				t.Fatalf("question %d: %d %s", i, code, body)
			}
		}
		code, body := e.ask(user, chat)
		eb := errCode(t, []byte(body))
		if code != http.StatusPaymentRequired || eb.Error.Code != "plan_limit" {
			t.Fatalf("third: %d %s", code, body)
		}
		reset, err := time.Parse(time.RFC3339, eb.Error.ResetAt)
		if err != nil || reset.Day() != 1 {
			t.Errorf("resetAt %q", eb.Error.ResetAt)
		}
		code, b := e.do(http.MethodGet, "/v1/assistant/usage", user, "")
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil || code != http.StatusOK {
			t.Fatalf("usage: %d %s", code, b)
		}
		month, _ := v["month"].(map[string]any)
		if v["enabled"] != true || v["plan"] != "team" || v["model"] != "claude-sonnet-5-5" || v["modelName"] != "Sonnet 5.5" ||
			v["aiChanges"] != "alerts" || month["limit"] != float64(2) || month["used"] != float64(2) || month["pooled"] != true {
			t.Errorf("usage = %s", b)
		}
		if day, _ := v["day"].(map[string]any); day["limit"] != float64(10) || day["resetAt"] == nil {
			t.Errorf("usage day = %s", b)
		}
	})
}
