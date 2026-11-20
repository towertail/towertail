package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

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

// mcpPost sends a JSON-RPC body to /mcp. It fails the test when the reply
// does not end.
func (e *keyEnv) mcpPost(t *testing.T, method, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
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
