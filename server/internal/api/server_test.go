package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/store"
)

// testEnv is a Server on a temp Bolt store with a real token issuer.
// Bolt skips fsync, so tests run fast.
type testEnv struct {
	t      *testing.T
	srv    *Server
	st     *store.BoltStore
	issuer *auth.TokenIssuer
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.DB().NoSync = true
	t.Cleanup(func() { _ = st.Close() })
	return &testEnv{t: t, st: st, issuer: auth.NewIssuer(st)}
}

// serve fills the empty fields of d and makes the Server.
func (e *testEnv) serve(d Deps) *Server {
	if d.Config == nil {
		d.Config = &config.Config{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	d.Store = e.st
	d.Issuer = e.issuer
	e.srv = New(d)
	return e.srv
}

func (e *testEnv) do(method, path, token string, body []byte, header ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// issue gives the raw token of a new token.
func (e *testEnv) issue(org uuid.UUID, kind store.TokenKind, node *uuid.UUID) string {
	e.t.Helper()
	raw, _, err := e.issuer.Issue(context.Background(), org, kind, "test", node, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return raw
}

func decode(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
}

func wantCode(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("want %d, got %d: %s", code, rec.Code, rec.Body)
	}
}

func TestRouteRoles(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	srv := newRoleServer(t, nil)
	below := map[store.Role]store.Role{
		store.RoleViewer: "guest",
		store.RoleMember: store.RoleViewer,
		store.RoleAdmin:  store.RoleMember,
	}
	for _, r := range []struct {
		method, path string
		need         store.Role
	}{
		{"GET", "/v1/nodes", store.RoleViewer},
		{"GET", "/v1/nodes/" + id, store.RoleViewer},
		{"GET", "/v1/nodes/" + id + "/history", store.RoleViewer},
		{"GET", "/v1/nodes/" + id + "/processes", store.RoleViewer},
		{"GET", "/v1/nodes/" + id + "/processes/at", store.RoleViewer},
		{"GET", "/v1/nodes/" + id + "/processes/0123456789abcdef.bmFtZQ", store.RoleViewer},
		{"GET", "/v1/nodes/" + id + "/disks", store.RoleViewer},
		{"GET", "/v1/history", store.RoleViewer},
		{"GET", "/v1/fleet/hosts", store.RoleViewer},
		{"GET", "/v1/fleet/processes", store.RoleViewer},
		{"GET", "/v1/fleet/compare", store.RoleViewer},
		{"GET", "/v1/api-keys", store.RoleViewer},
		{"POST", "/v1/api-keys", store.RoleViewer},
		{"DELETE", "/v1/api-keys/" + id, store.RoleViewer},
		{"GET", "/v1/settings", store.RoleViewer},
		{"GET", "/v1/rules", store.RoleViewer},
		{"GET", "/v1/nodes/" + id + "/rules", store.RoleViewer},
		{"GET", "/v1/alerts", store.RoleViewer},
		{"GET", "/v1/alerts/recent", store.RoleViewer},
		{"GET", "/v1/sampler/versions", store.RoleViewer},
		{"GET", "/v1/nodes/" + id + "/disk-scans", store.RoleViewer},
		{"GET", "/v1/disk-scans/" + id, store.RoleViewer},
		{"GET", "/v1/disk-scans/" + id + "/result", store.RoleViewer},
		{"POST", "/v1/auth/logout", store.RoleViewer},
		{"GET", "/v1/stream", store.RoleViewer},

		{"POST", "/v1/alerts/" + id + "/ack", store.RoleMember},
		{"POST", "/v1/alerts/" + id + "/snooze", store.RoleMember},
		{"DELETE", "/v1/alerts/" + id + "/ack", store.RoleMember},
		{"DELETE", "/v1/alerts/" + id + "/snooze", store.RoleMember},
		{"POST", "/v1/nodes/" + id + "/snooze", store.RoleMember},
		{"DELETE", "/v1/nodes/" + id + "/snooze", store.RoleMember},
		{"PUT", "/v1/mutes", store.RoleMember},
		{"POST", "/v1/nodes/" + id + "/disk-scans", store.RoleMember},
		{"POST", "/v1/disk-scans/" + id + "/cancel", store.RoleMember},

		{"POST", "/v1/nodes", store.RoleAdmin},
		{"PUT", "/v1/nodes/" + id, store.RoleAdmin},
		{"DELETE", "/v1/nodes/" + id, store.RoleAdmin},
		{"POST", "/v1/nodes/" + id + "/kill-process", store.RoleAdmin},
		{"POST", "/v1/alerts/" + id + "/ignore", store.RoleAdmin},
		{"DELETE", "/v1/alerts/" + id + "/ignore", store.RoleAdmin},
		{"PUT", "/v1/settings", store.RoleAdmin},
		{"PUT", "/v1/nodes/" + id + "/rules", store.RoleAdmin},
		{"POST", "/v1/rules/import", store.RoleAdmin},
		{"GET", "/v1/rules/backups", store.RoleAdmin},
		{"POST", "/v1/rules/backups/" + id + "/restore", store.RoleAdmin},
		{"GET", "/v1/channels", store.RoleAdmin},
		{"POST", "/v1/channels", store.RoleAdmin},
		{"GET", "/v1/channels/" + id, store.RoleAdmin},
		{"PUT", "/v1/channels/" + id, store.RoleAdmin},
		{"DELETE", "/v1/channels/" + id, store.RoleAdmin},
		{"POST", "/v1/channels/" + id + "/test", store.RoleAdmin},
		{"POST", "/v1/sampler/enroll", store.RoleAdmin},
		{"POST", "/v1/sampler/join-tokens", store.RoleAdmin},
		{"GET", "/v1/sampler/tokens", store.RoleAdmin},
		{"DELETE", "/v1/sampler/tokens/" + id, store.RoleAdmin},
	} {
		h := func(role store.Role) map[string]string {
			return map[string]string{"X-Test-Kind": "user", "X-Test-User": "1", "X-Test-Role": string(role)}
		}
		if rec := srv.call(r.method, r.path, h(below[r.need])); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with role %q: %d, want 403", r.method, r.path, below[r.need], rec.Code)
		}
		if rec := srv.call(r.method, r.path, h(r.need)); rec.Code == http.StatusForbidden {
			t.Errorf("%s %s with role %q: 403", r.method, r.path, r.need)
		}
	}
}
