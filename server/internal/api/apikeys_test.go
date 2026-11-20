package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// userRoles gives a role for each user. An unknown user has no membership.
type userRoles map[uuid.UUID]store.Role

func (r userRoles) Role(_ context.Context, _, user uuid.UUID) (store.Role, error) {
	if role, ok := r[user]; ok {
		return role, nil
	}
	return "", store.ErrNoMembership
}

// keyEnv is a server with real tokens and a role for each user.
type keyEnv struct {
	srv    *Server
	st     store.ConfigStore
	issuer *auth.TokenIssuer
	roles  userRoles
}

func newKeyEnv(t *testing.T, cfg config.HTTPConfig) *keyEnv {
	t.Helper()
	return newKeyEnvConfig(t, &config.Config{HTTP: cfg})
}

func newKeyEnvConfig(t *testing.T, cfg *config.Config) *keyEnv {
	t.Helper()
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &keyEnv{st: st, issuer: auth.NewIssuer(st), roles: userRoles{}}
	e.srv = New(Deps{
		Config: cfg,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:  st,
		Issuer: e.issuer,
		Roles:  e.roles,
	})
	return e
}

// login makes a user token for a new user with the role.
func (e *keyEnv) login(t *testing.T, role store.Role) (token string, user uuid.UUID) {
	t.Helper()
	user = uuid.New()
	e.roles[user] = role
	raw, _, err := e.issuer.Issue(context.Background(), store.ZeroOrg, store.TokenKindUser, "login", nil, &user)
	if err != nil {
		t.Fatal(err)
	}
	return raw, user
}

func (e *keyEnv) do(method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func (e *keyEnv) create(t *testing.T, token, label string) wire.APIKeyCreated {
	t.Helper()
	rec := e.do(http.MethodPost, "/v1/api-keys", token, `{"label":"`+label+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var out wire.APIKeyCreated
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *keyEnv) list(t *testing.T, token string) []wire.APIKey {
	t.Helper()
	rec := e.do(http.MethodGet, "/v1/api-keys", token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var out []wire.APIKey
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAPIKeysAPI(t *testing.T) {
	e := newKeyEnv(t, config.HTTPConfig{})
	member, memberID := e.login(t, store.RoleMember)
	other, _ := e.login(t, store.RoleMember)
	admin, _ := e.login(t, store.RoleAdmin)

	k := e.create(t, member, "ci")
	if !strings.HasPrefix(k.Token, "tt_") || k.Label != "ci" || k.UserID == nil || *k.UserID != memberID {
		t.Fatalf("created = %+v", k)
	}
	tok, err := e.issuer.Verify(context.Background(), k.Token)
	if err != nil || tok.Kind != store.TokenKindAPI || tok.UserID == nil || *tok.UserID != memberID {
		t.Fatalf("stored token = %+v, %v", tok, err)
	}
	theirs := e.create(t, other, "theirs")

	got := e.list(t, member)
	if len(got) != 1 || got[0].ID != k.ID {
		t.Fatalf("member list = %+v, want only own key", got)
	}
	if body := e.do(http.MethodGet, "/v1/api-keys", member, "").Body.String(); strings.Contains(body, k.Token) || strings.Contains(body, "hash") {
		t.Fatalf("the list shows a secret: %s", body)
	}
	if got := e.list(t, admin); len(got) != 2 {
		t.Fatalf("admin list = %+v, want 2 keys", got)
	}

	if rec := e.do(http.MethodPost, "/v1/api-keys", member, `{"label":""}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty label: %d, want 400", rec.Code)
	}
	if rec := e.do(http.MethodDelete, "/v1/api-keys/"+theirs.ID.String(), member, ""); rec.Code != http.StatusForbidden {
		t.Errorf("revoke of another user: %d, want 403", rec.Code)
	}
	_, sampler, err := e.issuer.Issue(context.Background(), store.ZeroOrg, store.TokenKindSampler, "s", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec := e.do(http.MethodDelete, "/v1/api-keys/"+sampler.ID.String(), admin, ""); rec.Code != http.StatusNotFound {
		t.Errorf("revoke of a sampler token: %d, want 404", rec.Code)
	}
	if rec := e.do(http.MethodDelete, "/v1/api-keys/"+k.ID.String(), member, ""); rec.Code != http.StatusNoContent {
		t.Errorf("revoke own: %d, want 204", rec.Code)
	}
	if _, err := e.issuer.Verify(context.Background(), k.Token); err == nil {
		t.Error("the revoked key still works")
	}
	if rec := e.do(http.MethodDelete, "/v1/api-keys/"+theirs.ID.String(), admin, ""); rec.Code != http.StatusNoContent {
		t.Errorf("admin revoke: %d, want 204", rec.Code)
	}
}

func TestAPIKeyLimit(t *testing.T) {
	e := newKeyEnv(t, config.HTTPConfig{})
	member, _ := e.login(t, store.RoleMember)
	other, _ := e.login(t, store.RoleMember)
	for i := 0; i < maxAPIKeys; i++ {
		e.create(t, member, "k")
	}
	rec := e.do(http.MethodPost, "/v1/api-keys", member, `{"label":"k"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "20 API keys") {
		t.Fatalf("21st key: %d %s, want 409", rec.Code, rec.Body)
	}
	e.create(t, other, "k")
}

func TestAPIKeyReadOnly(t *testing.T) {
	e := newKeyEnv(t, config.HTTPConfig{})
	member, _ := e.login(t, store.RoleOwner)
	key := e.create(t, member, "ro").Token
	id := uuid.NewString()

	// The test server has no ClickHouse, so a fleet read answers 503 after
	// it passes the auth checks.
	for path, want := range map[string]int{"/v1/nodes": http.StatusOK, "/v1/fleet/hosts": http.StatusServiceUnavailable} {
		if rec := e.do(http.MethodGet, path, key, ""); rec.Code != want {
			t.Errorf("GET %s: %d, want %d", path, rec.Code, want)
		}
	}
	for _, r := range []struct{ method, path string }{
		{"PUT", "/v1/settings"},
		{"POST", "/v1/alerts/" + id + "/ack"},
		{"POST", "/v1/api-keys"},
		{"GET", "/v1/api-keys"},
		{"DELETE", "/v1/api-keys/" + id},
		{"GET", "/v1/stream"},
		{"POST", "/v1/auth/logout"},
		{"POST", "/v1/sampler/enroll"},
	} {
		if rec := e.do(r.method, r.path, key, "{}"); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d, want 403", r.method, r.path, rec.Code)
		}
	}
}

// A key of a user that left the org stops working.
func TestAPIKeyNeedsMembership(t *testing.T) {
	e := newKeyEnv(t, config.HTTPConfig{})
	owner, user := e.login(t, store.RoleOwner)
	key := e.create(t, owner, "k").Token
	if rec := e.do(http.MethodGet, "/v1/nodes", key, ""); rec.Code != http.StatusOK {
		t.Fatalf("member key: %d", rec.Code)
	}
	delete(e.roles, user)
	if rec := e.do(http.MethodGet, "/v1/nodes", key, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("key after removal: %d, want 403", rec.Code)
	}
}

// A key from the CLI has no user. It still reads as a viewer.
func TestAPIKeyWithoutUser(t *testing.T) {
	e := newKeyEnv(t, config.HTTPConfig{})
	raw, _, err := e.issuer.Issue(context.Background(), store.ZeroOrg, store.TokenKindAPI, "cli", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec := e.do(http.MethodGet, "/v1/nodes", raw, ""); rec.Code != http.StatusOK {
		t.Fatalf("GET nodes: %d", rec.Code)
	}
	if rec := e.do(http.MethodPut, "/v1/settings", raw, "{}"); rec.Code != http.StatusForbidden {
		t.Fatalf("PUT settings: %d, want 403", rec.Code)
	}
}
