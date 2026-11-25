package api

import (
	"net/http"
	"testing"

	"github.com/towertail/towertail/server/store"
)

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
		{"GET", "/v1/nodes/" + id + "/disks", store.RoleViewer},
		{"GET", "/v1/history", store.RoleViewer},
		{"GET", "/v1/fleet/hosts", store.RoleViewer},
		{"GET", "/v1/fleet/processes", store.RoleViewer},
		{"GET", "/v1/fleet/compare", store.RoleViewer},
		{"GET", "/v1/api-keys", store.RoleViewer},
		{"POST", "/v1/api-keys", store.RoleViewer},
		{"DELETE", "/v1/api-keys/" + id, store.RoleViewer},
		{"GET", "/v1/settings", store.RoleViewer},
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
