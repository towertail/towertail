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
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/alerter"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

func TestSamplerTokens(t *testing.T) {
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	issuer := auth.NewIssuer(st)
	user := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authctx.Set(c, store.Token{OrgID: store.ZeroOrg, Kind: store.TokenKindUser})
			return next(c)
		}
	}
	srv := New(Deps{
		Config:     &config.Config{},
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:      st,
		Issuer:     issuer,
		Middleware: []echo.MiddlewareFunc{user},
	})
	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	list := func() []wire.SamplerToken {
		rec := do(http.MethodGet, "/v1/sampler/tokens", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body)
		}
		var out []wire.SamplerToken
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	if got := list(); len(got) != 0 {
		t.Fatalf("empty list: %v", got)
	}
	rec := do(http.MethodPost, "/v1/sampler/enroll", `{"display_name":"web-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll: %d %s", rec.Code, rec.Body)
	}
	var enr wire.EnrollResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &enr)
	// A user token and a token of another org do not show.
	ctx := context.Background()
	if _, _, err := issuer.Issue(ctx, store.ZeroOrg, store.TokenKindUser, "me", nil, nil); err != nil {
		t.Fatal(err)
	}
	_, other, err := issuer.Issue(ctx, uuid.New(), store.TokenKindSampler, "other", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := list()
	if len(got) != 1 || got[0].Label != "web-1" || got[0].NodeID == nil || *got[0].NodeID != enr.NodeID {
		t.Fatalf("list: %+v", got)
	}
	if strings.Contains(do(http.MethodGet, "/v1/sampler/tokens", "").Body.String(), "hash") {
		t.Fatal("the list shows the hash")
	}
	if rec := do(http.MethodDelete, "/v1/sampler/tokens/"+other.ID.String(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete other org: %d", rec.Code)
	}
	if rec := do(http.MethodDelete, "/v1/sampler/tokens/"+got[0].ID.String(), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := issuer.Verify(ctx, enr.SamplerToken); err == nil {
		t.Fatal("the revoked token still works")
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("list after delete: %v", got)
	}
	if _, err := st.GetNode(ctx, store.ZeroOrg, enr.NodeID); err != nil {
		t.Fatalf("the node must stay: %v", err)
	}
}

// enrollEnv is a server with a user for the ZeroOrg on every request.
type enrollEnv struct {
	t      *testing.T
	srv    *Server
	st     *store.BoltStore
	issuer *auth.TokenIssuer
}

func newEnrollEnv(t *testing.T, cfg config.Config, limits Limits) *enrollEnv {
	t.Helper()
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	issuer := auth.NewIssuer(st)
	user := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authctx.Set(c, store.Token{OrgID: store.ZeroOrg, Kind: store.TokenKindUser})
			return next(c)
		}
	}
	d := Deps{
		Config:     &cfg,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:      st,
		Issuer:     issuer,
		Middleware: []echo.MiddlewareFunc{user},
	}
	if limits != nil {
		d.Limits = limits
	}
	return &enrollEnv{t: t, srv: New(d), st: st, issuer: issuer}
}

func (e *enrollEnv) do(method, target, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func (e *enrollEnv) enroll(body string) (*httptest.ResponseRecorder, wire.EnrollResponse) {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/v1/sampler/enroll", body)
	var out wire.EnrollResponse
	if rec.Code == http.StatusOK {
		decode(e.t, rec.Body.Bytes(), &out)
	}
	return rec, out
}

func (e *enrollEnv) joinToken() string {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/v1/sampler/join-tokens", `{"display_name":"new"}`)
	wantCode(e.t, rec, http.StatusCreated)
	var out wire.JoinTokenResponse
	decode(e.t, rec.Body.Bytes(), &out)
	return out.Token
}

func (e *enrollEnv) join(token, machine string) (*httptest.ResponseRecorder, wire.EnrollResponse) {
	e.t.Helper()
	body, _ := json.Marshal(wire.JoinRequest{JoinToken: token, DisplayName: "web-1", MachineID: machine})
	rec := e.do(http.MethodPost, "/v1/sampler/join", string(body))
	var out wire.EnrollResponse
	if rec.Code == http.StatusOK {
		decode(e.t, rec.Body.Bytes(), &out)
	}
	return rec, out
}

func (e *enrollEnv) nodes() []store.Node {
	e.t.Helper()
	list, err := e.st.ListNodes(context.Background(), store.ZeroOrg)
	if err != nil {
		e.t.Fatal(err)
	}
	return list
}

func TestEnroll_MakesNodeWithGivenID(t *testing.T) {
	e := newEnrollEnv(t, config.Config{}, nil)
	id := uuid.New()
	rec, out := e.enroll(`{"display_name":"mac","node_id":"` + id.String() + `"}`)
	wantCode(t, rec, http.StatusOK)
	if out.NodeID != id {
		t.Fatalf("node id %v, want %v", out.NodeID, id)
	}
	n, err := e.st.GetNode(context.Background(), store.ZeroOrg, id)
	if err != nil || n.DisplayName != "mac" || !n.Enabled {
		t.Fatalf("node: %+v, %v", n, err)
	}
	tok, err := e.issuer.Verify(context.Background(), out.SamplerToken)
	if err != nil || tok.NodeID == nil || *tok.NodeID != id {
		t.Fatalf("token: %+v, %v", tok, err)
	}
	// A second call binds to the same node.
	rec, again := e.enroll(`{"display_name":"mac","node_id":"` + id.String() + `"}`)
	wantCode(t, rec, http.StatusOK)
	if again.NodeID != id || len(e.nodes()) != 1 {
		t.Fatalf("second call: %v, %d nodes", again.NodeID, len(e.nodes()))
	}
}

func TestEnroll_NodeIDLimit(t *testing.T) {
	e := newEnrollEnv(t, config.Config{}, maxNodes(1))
	if err := e.st.PutNode(context.Background(), store.ZeroOrg, store.Node{ID: uuid.New(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	rec, _ := e.enroll(`{"display_name":"mac","node_id":"` + uuid.NewString() + `"}`)
	wantCode(t, rec, http.StatusPaymentRequired)
	if len(e.nodes()) != 1 {
		t.Fatalf("%d nodes", len(e.nodes()))
	}
}

func TestEnroll_MachineIDDedupe(t *testing.T) {
	e := newEnrollEnv(t, config.Config{}, nil)
	_, a := e.enroll(`{"display_name":"web-1","machine_id":"m-1"}`)
	_, b := e.enroll(`{"display_name":"web-1","machine_id":"m-1"}`)
	if a.NodeID != b.NodeID || len(e.nodes()) != 1 {
		t.Fatalf("nodes %v and %v, %d in the store", a.NodeID, b.NodeID, len(e.nodes()))
	}
	_, c := e.enroll(`{"display_name":"web-2","machine_id":"m-2"}`)
	if c.NodeID == a.NodeID || len(e.nodes()) != 2 {
		t.Fatalf("other machine: %v, %d nodes", c.NodeID, len(e.nodes()))
	}
	// No machine id: each call makes a node.
	e.enroll(`{"display_name":"x"}`)
	e.enroll(`{"display_name":"x"}`)
	if len(e.nodes()) != 4 {
		t.Fatalf("%d nodes", len(e.nodes()))
	}
}

func TestEnroll_Endpoint(t *testing.T) {
	e := newEnrollEnv(t, config.Config{}, nil)
	_, out := e.enroll(`{"display_name":"a"}`)
	if out.Endpoint != "http://example.com" {
		t.Errorf("endpoint %q", out.Endpoint)
	}
	e = newEnrollEnv(t, config.Config{HTTP: config.HTTPConfig{PublicURL: "https://tt.example.org/"}}, nil)
	_, out = e.enroll(`{"display_name":"a"}`)
	if out.Endpoint != "https://tt.example.org" {
		t.Errorf("endpoint %q", out.Endpoint)
	}
}

func TestJoinToken_Create(t *testing.T) {
	e := newEnrollEnv(t, config.Config{}, maxNodes(0))
	wantCode(t, e.do(http.MethodPost, "/v1/sampler/join-tokens", ``), http.StatusPaymentRequired)
	e = newEnrollEnv(t, config.Config{}, maxNodes(1))
	rec := e.do(http.MethodPost, "/v1/sampler/join-tokens", ``)
	wantCode(t, rec, http.StatusCreated)
	var out wire.JoinTokenResponse
	decode(t, rec.Body.Bytes(), &out)
	if !strings.HasPrefix(out.Token, "tt_join_") {
		t.Errorf("token %q", out.Token)
	}
	if d := time.Until(out.ExpiresAt); d < 59*time.Minute || d > time.Hour {
		t.Errorf("expires in %v", d)
	}
}

func TestJoin_OneUse(t *testing.T) {
	e := newEnrollEnv(t, config.Config{}, nil)
	tok := e.joinToken()
	rec, out := e.join(tok, "m-1")
	wantCode(t, rec, http.StatusOK)
	n, err := e.st.GetNode(context.Background(), store.ZeroOrg, out.NodeID)
	if err != nil || n.DisplayName != "web-1" || n.MachineID != "m-1" {
		t.Fatalf("node: %+v, %v", n, err)
	}
	st, err := e.issuer.Verify(context.Background(), out.SamplerToken)
	if err != nil || st.Kind != store.TokenKindSampler || *st.NodeID != out.NodeID {
		t.Fatalf("sampler token: %+v, %v", st, err)
	}
	rec, _ = e.join(tok, "m-2")
	wantCode(t, rec, http.StatusUnauthorized)
	if len(e.nodes()) != 1 {
		t.Fatalf("%d nodes", len(e.nodes()))
	}
}

func TestJoin_Invalid(t *testing.T) {
	e := newEnrollEnv(t, config.Config{}, nil)
	rec, _ := e.join("tt_join_unknown", "")
	wantCode(t, rec, http.StatusUnauthorized)
	// A sampler token is not a join token.
	raw, _, err := e.issuer.Issue(context.Background(), store.ZeroOrg, store.TokenKindSampler, "s", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ = e.join(raw, "")
	wantCode(t, rec, http.StatusUnauthorized)
	wantCode(t, e.do(http.MethodPost, "/v1/sampler/join", `{"join_token":"x"}`), http.StatusBadRequest)
}

func TestJoin_RateLimit(t *testing.T) {
	e := newEnrollEnv(t, config.Config{}, nil)
	var last int
	for range 12 {
		rec, _ := e.join("tt_join_unknown", "")
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("12th call: %d, want 429", last)
	}
}

func TestJoin_Expired(t *testing.T) {
	e := newEnrollEnv(t, config.Config{}, nil)
	raw, tok, err := store.NewToken(store.ZeroOrg, store.TokenKindJoin, "old")
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	tok.ExpiresAt = &past
	if err := e.st.PutToken(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	rec, _ := e.join(raw, "")
	wantCode(t, rec, http.StatusUnauthorized)
}

func TestJoin_PlanFull(t *testing.T) {
	e := newEnrollEnv(t, config.Config{}, maxNodes(1))
	tok := e.joinToken()
	if err := e.st.PutNode(context.Background(), store.ZeroOrg, store.Node{ID: uuid.New(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	rec, _ := e.join(tok, "")
	wantCode(t, rec, http.StatusPaymentRequired)
	if _, err := e.issuer.Verify(context.Background(), tok); err != nil {
		t.Fatalf("the join token is gone: %v", err)
	}
}

func TestJoinToken_KindGuard(t *testing.T) {
	e := newIngestEnv(t, config.Config{}, nil, nil)
	raw, _, err := e.issuer.IssueJoin(context.Background(), store.ZeroOrg, nil, "j", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, e.post(raw, []byte(sampleLine)), http.StatusForbidden)
}

func TestUpdateNode_KeepsMachineID(t *testing.T) {
	e := newEnrollEnv(t, config.Config{}, nil)
	_, out := e.enroll(`{"display_name":"web-1","machine_id":"m-1"}`)
	path := "/v1/nodes/" + out.NodeID.String()
	wantCode(t, e.do(http.MethodPut, path, `{"displayName":"renamed","kind":"ssh","enabled":true}`), http.StatusOK)
	var n store.Node
	decode(t, e.do(http.MethodGet, path, "").Body.Bytes(), &n)
	if n.MachineID != "m-1" || n.DisplayName != "renamed" {
		t.Fatalf("node: %+v", n)
	}
	if n.CreatedAt.IsZero() {
		t.Error("created_at is lost")
	}
}

// registeredStore records the events of the alerter.
type registeredStore struct {
	mu     sync.Mutex
	events []clickhouse.EventRow
}

func (s *registeredStore) WriteEvent(_ context.Context, e clickhouse.EventRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

func (*registeredStore) LastTints(context.Context, time.Time) ([]clickhouse.TintRow, error) {
	return nil, nil
}

func (*registeredStore) LastSeen(context.Context, time.Time) ([]clickhouse.NodeSeen, error) {
	return nil, nil
}

func (*registeredStore) Registrations(context.Context, time.Time) ([]clickhouse.NodeSeen, error) {
	return nil, nil
}

func TestEnrollAndJoin_ExpectFirstSample(t *testing.T) {
	e := newEnrollEnv(t, config.Config{}, nil)
	rs := &registeredStore{}
	e.srv.alerter = alerter.New(nil, nil, nil, rs, nil)

	_, a := e.enroll(`{"display_name":"a","machine_id":"m-1"}`)
	e.enroll(`{"display_name":"a","machine_id":"m-1"}`) // same node: no new event
	_, b := e.join(e.joinToken(), "m-2")

	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.events) != 2 {
		t.Fatalf("events = %+v", rs.events)
	}
	for i, id := range []uuid.UUID{a.NodeID, b.NodeID} {
		ev := rs.events[i]
		if ev.Kind != "registered" || ev.NodeID != id || ev.OrgID != store.ZeroOrg || ev.Tint != "ok" {
			t.Errorf("event %d = %+v", i, ev)
		}
	}
}
