package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/store"
)

func TestAPIKeyRateLimit(t *testing.T) {
	e := newKeyEnv(t, config.HTTPConfig{APIKeyRateLimitRPS: 1, APIKeyRateLimitBurst: 2})
	user, _ := e.login(t, store.RoleOwner)
	keyA := e.create(t, user, "a").Token
	keyB := e.create(t, user, "b").Token

	for i := 0; i < 2; i++ {
		if rec := e.do(http.MethodGet, "/v1/nodes", keyA, ""); rec.Code != http.StatusOK {
			t.Fatalf("call %d: %d", i+1, rec.Code)
		}
	}
	rec := e.do(http.MethodGet, "/v1/nodes", keyA, "")
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "rate_limited") {
		t.Fatalf("third call: %d %s, want 429 rate_limited", rec.Code, rec.Body)
	}
	if rec := e.do(http.MethodGet, "/v1/nodes", keyB, ""); rec.Code != http.StatusOK {
		t.Fatalf("second key: %d, want its own budget", rec.Code)
	}
	for i := 0; i < 10; i++ {
		if rec := e.do(http.MethodGet, "/v1/nodes", user, ""); rec.Code != http.StatusOK {
			t.Fatalf("user call %d: %d, want no limit", i+1, rec.Code)
		}
	}
}

func TestKeyLimitersDropIdle(t *testing.T) {
	now := time.Now()
	k := newKeyLimiters(1, 1)
	k.now = func() time.Time { return now }
	id := uuid.New()
	if !k.allow(id) || k.allow(id) {
		t.Fatal("burst of 1: want one call allowed")
	}
	now = now.Add(keyLimiterTTL + time.Minute)
	if !k.allow(uuid.New()) {
		t.Fatal("new key blocked")
	}
	if _, ok := k.limiters[id]; ok {
		t.Fatal("the idle limiter stays")
	}
}
