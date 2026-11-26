package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/store"
)

const (
	mcpToolsList = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	mcpListHosts = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_hosts","arguments":{}}}`
)

func newMCPEnv(t *testing.T, cfg config.HTTPConfig, enabled bool) *keyEnv {
	t.Helper()
	return newKeyEnvConfig(t, &config.Config{HTTP: cfg, MCP: config.MCPConfig{Enabled: enabled}})
}

// mcpPost sends a JSON-RPC body to /mcp. mcpPostTo takes the path. It fails the test when the reply
// does not end.
func (e *keyEnv) mcpPost(t *testing.T, method, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	return e.mcpPostTo(t, "/mcp", method, token, body)
}

func (e *keyEnv) mcpPostTo(t *testing.T, path, method, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.srv.Handler().ServeHTTP(rec, req)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the reply does not end")
	}
	return rec
}

func TestMCPHTTP(t *testing.T) {
	e := newMCPEnv(t, config.HTTPConfig{}, true)
	user, _ := e.login(t, store.RoleViewer)
	key := e.create(t, user, "claude").Token
	sampler, _, err := e.issuer.Issue(context.Background(), store.ZeroOrg, store.TokenKindSampler, "s", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	for name, token := range map[string]string{"api key": key, "user token": user} {
		rec := e.mcpPost(t, http.MethodPost, token, mcpToolsList)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || !strings.Contains(rec.Body.String(), `"list_hosts"`) {
			t.Errorf("%s: tools/list = %d %s %s", name, rec.Code, rec.Header().Get("Content-Type"), rec.Body)
		}
		if n := strings.Count(rec.Body.String(), `"readOnlyHint"`); n != 25 {
			t.Errorf("%s: tools/list has %d tools, want 25 (18 read and 7 write)", name, n)
		}
	}

	// The test server has no ClickHouse.
	rec := e.mcpPost(t, http.MethodPost, key, mcpListHosts)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"isError":true`) || !strings.Contains(rec.Body.String(), "query: history is off") {
		t.Errorf("list_hosts = %d %s, want the tool error", rec.Code, rec.Body)
	}

	for _, tc := range []struct {
		name, method, token string
		want                int
	}{
		{"GET", http.MethodGet, key, http.StatusMethodNotAllowed},
		{"DELETE", http.MethodDelete, key, http.StatusMethodNotAllowed},
		{"no token", http.MethodPost, "", http.StatusUnauthorized},
		{"bad token", http.MethodPost, "tt_bogus", http.StatusUnauthorized},
		{"sampler token", http.MethodPost, sampler, http.StatusForbidden},
	} {
		if rec := e.mcpPost(t, tc.method, tc.token, mcpToolsList); rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, rec.Code, rec.Body, tc.want)
		}
	}
}

func TestMCPHTTPWrites(t *testing.T) {
	e := newOAuthEnv(t, config.HTTPConfig{})
	user, owner := e.login(t, store.RoleOwner)
	_, viewer := e.login(t, store.RoleViewer)
	key := e.create(t, user, "claude").Token
	withScopes := func(u *uuid.UUID) string {
		return e.oauthToken(t, u, func(tk *store.Token) { tk.Scopes = store.ParseScopes("fleet:read alerts:write rules:write") })
	}
	ack := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ack_alert","arguments":{"alert_id":"` + uuid.NewString() + `"}}}`
	for _, tc := range []struct{ name, token, want string }{
		{"api key", key, "API keys can only read"},
		{"user token", user, "no write scopes"},
		{"read-only oauth token", e.oauthToken(t, &owner, nil), "does not have the scope alerts:write"},
		{"viewer", withScopes(&viewer), "Your role is viewer"},
		// The right check passes. The test server has no ClickHouse, so the alert is unknown.
		{"owner", withScopes(&owner), "alert not found"},
	} {
		rec := e.mcpPost(t, http.MethodPost, tc.token, ack)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"isError":true`) || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: ack_alert = %d %s, want the tool error %q", tc.name, rec.Code, rec.Body, tc.want)
		}
	}
}

func TestMCPRateLimit(t *testing.T) {
	e := newMCPEnv(t, config.HTTPConfig{APIKeyRateLimitRPS: 1, APIKeyRateLimitBurst: 2}, true)
	user, _ := e.login(t, store.RoleOwner)
	key := e.create(t, user, "claude").Token
	for i := range 2 {
		if rec := e.mcpPost(t, http.MethodPost, key, mcpToolsList); rec.Code != http.StatusOK {
			t.Fatalf("call %d: %d", i+1, rec.Code)
		}
	}
	if rec := e.mcpPost(t, http.MethodPost, key, mcpToolsList); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third call: %d %s, want 429", rec.Code, rec.Body)
	}
}

func TestMCPDisabled(t *testing.T) {
	e := newMCPEnv(t, config.HTTPConfig{}, false)
	user, _ := e.login(t, store.RoleViewer)
	if rec := e.mcpPost(t, http.MethodPost, user, mcpToolsList); rec.Code != http.StatusNotFound {
		t.Fatalf("MCP off: %d %s, want 404", rec.Code, rec.Body)
	}
}

func TestMCPNoMembership(t *testing.T) {
	e := newMCPEnv(t, config.HTTPConfig{}, true)
	// A key of a user with no role in the org gets 403, as on the REST routes.
	ghost := uuid.New()
	raw, _, err := e.issuer.Issue(context.Background(), store.ZeroOrg, store.TokenKindAPI, "ghost", nil, &ghost)
	if err != nil {
		t.Fatal(err)
	}
	if rec := e.mcpPost(t, http.MethodPost, raw, mcpToolsList); rec.Code != http.StatusForbidden {
		t.Fatalf("key of a non-member: %d %s, want 403", rec.Code, rec.Body)
	}
}

func TestMCPV1HTTP(t *testing.T) {
	e := newMCPEnv(t, config.HTTPConfig{}, true)
	user, _ := e.login(t, store.RoleViewer)
	key := e.create(t, user, "claude").Token

	rec := e.mcpPostTo(t, "/mcp/v1", http.MethodPost, key, mcpToolsList)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"list_hosts"`) || !strings.Contains(rec.Body.String(), `"destructiveHint"`) {
		t.Errorf("tools/list = %d %s", rec.Code, rec.Body)
	}
	if rec := e.mcpPostTo(t, "/mcp/v1", http.MethodGet, key, mcpToolsList); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d %s, want 405", rec.Code, rec.Body)
	}
	if rec := e.mcpPostTo(t, "/mcp/v1", http.MethodPost, "", mcpToolsList); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d %s, want 401", rec.Code, rec.Body)
	}

	off := newMCPEnv(t, config.HTTPConfig{}, false)
	offUser, _ := off.login(t, store.RoleViewer)
	if rec := off.mcpPostTo(t, "/mcp/v1", http.MethodPost, offUser, mcpToolsList); rec.Code != http.StatusNotFound {
		t.Errorf("MCP off: %d %s, want 404", rec.Code, rec.Body)
	}
}

const (
	testResource = "https://api.example.com/mcp"
	testIssuer   = "https://api.example.com"
)

func newOAuthEnv(t *testing.T, cfg config.HTTPConfig) *keyEnv {
	t.Helper()
	return newKeyEnvConfig(t, &config.Config{HTTP: cfg, MCP: config.MCPConfig{
		Enabled: true, ResourceURL: testResource, AuthorizationServer: testIssuer, DocsURL: "https://towertail.com/docs/mcp",
	}})
}

// oauthToken stores an oauth token. The edit func changes it before the write.
func (e *keyEnv) oauthToken(t *testing.T, user *uuid.UUID, edit func(*store.Token)) string {
	t.Helper()
	raw, tok, err := store.NewToken(store.ZeroOrg, store.TokenKindOAuth, "Claude")
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(time.Hour)
	tok.UserID, tok.ExpiresAt, tok.Audience = user, &exp, testResource
	tok.Scopes = store.ParseScopes("")
	if edit != nil {
		edit(&tok)
	}
	if err := e.st.PutToken(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMCPChallengeHeader(t *testing.T) {
	e := newOAuthEnv(t, config.HTTPConfig{})
	for _, tc := range []struct{ path, token, want string }{
		{"/mcp", "", `Bearer resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/mcp", scope="fleet:read"`},
		{"/mcp/v1", "", `Bearer resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/mcp/v1", scope="fleet:read"`},
		{"/mcp", "tt_bogus", `Bearer resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/mcp", scope="fleet:read", error="invalid_token"`},
	} {
		rec := e.mcpPostTo(t, tc.path, http.MethodPost, tc.token, mcpToolsList)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != tc.want {
			t.Errorf("%s %q: %d %q, want 401 %q", tc.path, tc.token, rec.Code, rec.Header().Get("WWW-Authenticate"), tc.want)
		}
	}

	off := newMCPEnv(t, config.HTTPConfig{}, true)
	rec := off.mcpPost(t, http.MethodPost, "", mcpToolsList)
	if got := rec.Header().Get("WWW-Authenticate"); rec.Code != http.StatusUnauthorized || got != `Bearer realm="towertail"` {
		t.Errorf("OAuth off: %d %q", rec.Code, got)
	}
}

func TestMCPOAuthToken(t *testing.T) {
	e := newOAuthEnv(t, config.HTTPConfig{})
	_, member := e.login(t, store.RoleViewer)
	ghost := uuid.New()
	past := time.Now().Add(-time.Minute)

	good := e.oauthToken(t, &member, nil)
	for _, path := range []string{"/mcp", "/mcp/v1"} {
		if rec := e.mcpPostTo(t, path, http.MethodPost, good, mcpToolsList); rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s, want 200", path, rec.Code, rec.Body)
		}
	}
	v1 := e.oauthToken(t, &member, func(tk *store.Token) { tk.Audience = testResource + "/v1" })
	for _, tc := range []struct {
		name, token string
		want        int
	}{
		{"v1 audience", v1, http.StatusOK},
		{"wrong audience", e.oauthToken(t, &member, func(tk *store.Token) { tk.Audience = "https://other.example.com/mcp" }), http.StatusUnauthorized},
		{"no audience", e.oauthToken(t, &member, func(tk *store.Token) { tk.Audience = "" }), http.StatusUnauthorized},
		{"expired", e.oauthToken(t, &member, func(tk *store.Token) { tk.ExpiresAt = &past }), http.StatusUnauthorized},
		{"no user", e.oauthToken(t, nil, nil), http.StatusUnauthorized},
		{"no membership", e.oauthToken(t, &ghost, nil), http.StatusForbidden},
	} {
		if rec := e.mcpPost(t, http.MethodPost, tc.token, mcpToolsList); rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, rec.Code, rec.Body, tc.want)
		}
	}

	// With OAuth off, no audience can match.
	off := newMCPEnv(t, config.HTTPConfig{}, true)
	_, offMember := off.login(t, store.RoleViewer)
	if rec := off.mcpPost(t, http.MethodPost, off.oauthToken(t, &offMember, func(tk *store.Token) { tk.Audience = "" }), mcpToolsList); rec.Code != http.StatusUnauthorized {
		t.Errorf("OAuth off: %d %s, want 401", rec.Code, rec.Body)
	}
}

func TestOAuthTokenOnRESTRoute(t *testing.T) {
	e := newOAuthEnv(t, config.HTTPConfig{})
	_, member := e.login(t, store.RoleOwner)
	tok := e.oauthToken(t, &member, nil)
	for _, path := range []string{"/v1/nodes", "/v1/settings"} {
		if rec := e.do(http.MethodGet, path, tok, nil); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s: %d %s, want 403", path, rec.Code, rec.Body)
		}
	}
}

func TestMCPAPIKeysOff(t *testing.T) {
	e := newOAuthEnv(t, config.HTTPConfig{})
	user, member := e.login(t, store.RoleOwner)
	key := e.create(t, user, "claude").Token
	oauth := e.oauthToken(t, &member, nil)

	if rec := e.mcpPost(t, http.MethodPost, key, mcpToolsList); rec.Code != http.StatusOK {
		t.Fatalf("keys on: %d %s", rec.Code, rec.Body)
	}
	settings := store.DefaultServerSettings()
	settings.MCP.APIKeys = false
	if err := e.st.PutSettings(context.Background(), store.ZeroOrg, settings); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/mcp", "/mcp/v1"} {
		if rec := e.mcpPostTo(t, path, http.MethodPost, key, mcpToolsList); rec.Code != http.StatusForbidden {
			t.Errorf("%s key: %d %s, want 403", path, rec.Code, rec.Body)
		}
	}
	if rec := e.mcpPost(t, http.MethodPost, oauth, mcpToolsList); rec.Code != http.StatusOK {
		t.Errorf("oauth: %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := e.mcpPost(t, http.MethodPost, user, mcpToolsList); rec.Code != http.StatusOK {
		t.Errorf("user token: %d %s, want 200", rec.Code, rec.Body)
	}
}

func TestProtectedResourceMetadata(t *testing.T) {
	e := newOAuthEnv(t, config.HTTPConfig{})
	for path, resource := range map[string]string{
		"/.well-known/oauth-protected-resource/mcp":    testResource,
		"/.well-known/oauth-protected-resource":        testResource,
		"/.well-known/oauth-protected-resource/mcp/v1": testResource + "/v1",
	} {
		rec := e.do(http.MethodGet, path, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		servers, _ := got["authorization_servers"].([]any)
		scopes, _ := got["scopes_supported"].([]any)
		methods, _ := got["bearer_methods_supported"].([]any)
		if got["resource"] != resource || len(servers) != 1 || servers[0] != testIssuer || len(scopes) != 3 ||
			len(methods) != 1 || methods[0] != "header" || got["resource_name"] != "Towertail" ||
			got["resource_documentation"] != "https://towertail.com/docs/mcp" {
			t.Errorf("%s: %v", path, got)
		}
	}

	off := newMCPEnv(t, config.HTTPConfig{}, true)
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource/mcp/v1"} {
		if rec := off.do(http.MethodGet, path, "", nil); rec.Code != http.StatusNotFound {
			t.Errorf("OAuth off %s: %d, want 404", path, rec.Code)
		}
	}
}

func TestMCPPublicCORS(t *testing.T) {
	e := newOAuthEnv(t, config.HTTPConfig{CORSAllowOrigins: []string{"https://app.example.com"}})
	preflight := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodOptions, path, nil)
		req.Header.Set("Origin", "https://claude.ai")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization,content-type,mcp-protocol-version")
		rec := httptest.NewRecorder()
		e.srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	for _, path := range []string{"/mcp", "/mcp/v1", "/.well-known/oauth-protected-resource/mcp"} {
		rec := preflight(path)
		if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "*" ||
			rec.Header().Get("Access-Control-Allow-Credentials") != "" ||
			!strings.Contains(strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers")), "mcp-protocol-version") {
			t.Errorf("%s preflight: %d %v", path, rec.Code, rec.Header())
		}
	}
	// The error reply of a call exposes WWW-Authenticate.
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(mcpToolsList))
	req.Header.Set("Origin", "https://claude.ai")
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Access-Control-Allow-Origin") != "*" ||
		!strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), "WWW-Authenticate") {
		t.Errorf("POST: %d %v", rec.Code, rec.Header())
	}

	// REST routes keep the configured origins.
	req = httptest.NewRequest(http.MethodGet, "/v1/nodes", nil)
	req.Header.Set("Origin", "https://claude.ai")
	rec = httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("/v1/nodes allows %q for a foreign origin", got)
	}
	req.Header.Set("Origin", "https://app.example.com")
	rec = httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("/v1/nodes allows %q for the app origin", got)
	}
}

func TestMCPRateLimitByGrant(t *testing.T) {
	e := newOAuthEnv(t, config.HTTPConfig{APIKeyRateLimitRPS: 1, APIKeyRateLimitBurst: 2})
	_, member := e.login(t, store.RoleOwner)
	grant := uuid.New()
	withGrant := func(tk *store.Token) { tk.GrantID = &grant }
	first, second := e.oauthToken(t, &member, withGrant), e.oauthToken(t, &member, withGrant)
	if rec := e.mcpPost(t, http.MethodPost, first, mcpToolsList); rec.Code != http.StatusOK {
		t.Fatalf("call 1: %d", rec.Code)
	}
	if rec := e.mcpPost(t, http.MethodPost, second, mcpToolsList); rec.Code != http.StatusOK {
		t.Fatalf("call 2: %d", rec.Code)
	}
	if rec := e.mcpPost(t, http.MethodPost, first, mcpToolsList); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("call 3: %d %s, want 429", rec.Code, rec.Body)
	}
	// A token of another grant has its own budget.
	if rec := e.mcpPost(t, http.MethodPost, e.oauthToken(t, &member, nil), mcpToolsList); rec.Code != http.StatusOK {
		t.Errorf("other grant: %d, want 200", rec.Code)
	}
}

func TestTokenRoleOnContext(t *testing.T) {
	e := newOAuthEnv(t, config.HTTPConfig{})
	var got store.Role
	var ok bool
	e.srv.echo.POST("/test/role", func(c echo.Context) error {
		got, ok = authctx.RoleFromContext(c.Request().Context())
		return c.NoContent(http.StatusNoContent)
	}, e.srv.bearer(store.TokenKindOAuth, store.TokenKindAPI), tokenToRequest)
	_, admin := e.login(t, store.RoleAdmin)
	rec := e.do(http.MethodPost, "/test/role", e.oauthToken(t, &admin, nil), nil)
	if rec.Code != http.StatusNoContent || !ok || got != store.RoleAdmin {
		t.Fatalf("role = %q %v (%d), want admin", got, ok, rec.Code)
	}
}
