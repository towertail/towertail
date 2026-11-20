package api

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/store"
)

func TestFleetEndpoints(t *testing.T) {
	e := newKeyEnv(t, config.HTTPConfig{})
	user, _ := e.login(t, store.RoleViewer)
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/v1/fleet/hosts?sort=bogus", http.StatusBadRequest},
		{"/v1/fleet/hosts?limit=0", http.StatusBadRequest},
		{"/v1/fleet/hosts?limit=x", http.StatusBadRequest},
		{"/v1/fleet/processes?by=disk", http.StatusBadRequest},
		{"/v1/fleet/processes?node=nope", http.StatusBadRequest},
		{"/v1/fleet/processes?limit=-1", http.StatusBadRequest},
		{"/v1/fleet/compare?metric=procs", http.StatusBadRequest},
		{"/v1/fleet/compare?metric=bogus", http.StatusBadRequest},
		{"/v1/fleet/compare?window=5m", http.StatusBadRequest},
		{"/v1/fleet/compare?window=721h", http.StatusBadRequest},
		{"/v1/fleet/compare?window=abc", http.StatusBadRequest},
		{"/v1/fleet/compare?node=nope", http.StatusBadRequest},
		// Valid values reach requireCH. The server has no ClickHouse.
		{"/v1/fleet/hosts", http.StatusServiceUnavailable},
		{"/v1/fleet/hosts?sort=net&limit=500", http.StatusServiceUnavailable},
		{"/v1/fleet/processes?by=mem&node=" + uuid.NewString(), http.StatusServiceUnavailable},
		{"/v1/fleet/compare?metric=net_rx&window=24h", http.StatusServiceUnavailable},
	} {
		if rec := e.do(http.MethodGet, tc.path, user, ""); rec.Code != tc.want {
			t.Errorf("GET %s: %d %s, want %d", tc.path, rec.Code, rec.Body, tc.want)
		}
	}
}

func TestProcessesByValue(t *testing.T) {
	e := newKeyEnv(t, config.HTTPConfig{})
	user, _ := e.login(t, store.RoleViewer)
	path := "/v1/nodes/" + uuid.NewString() + "/processes?by=disk"
	if rec := e.do(http.MethodGet, path, user, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("by=disk: %d, want 400", rec.Code)
	}
}
