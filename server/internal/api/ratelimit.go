package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"golang.org/x/time/rate"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/store"
)

const (
	defaultKeyRPS   = 5
	defaultKeyBurst = 20
	keyLimiterTTL   = 10 * time.Minute
)

// keyLimiters keeps one rate limiter for each API key. It drops a limiter
// after keyLimiterTTL with no use.
type keyLimiters struct {
	rps   rate.Limit
	burst int
	now   func() time.Time

	mu        sync.Mutex
	limiters  map[uuid.UUID]*keyLimiter
	lastSweep time.Time
}

type keyLimiter struct {
	l    *rate.Limiter
	seen time.Time
}

// newKeyLimiters makes the limiters. A value of 0 or less gives the
// default.
func newKeyLimiters(rps float64, burst int) *keyLimiters {
	if rps <= 0 {
		rps = defaultKeyRPS
	}
	if burst <= 0 {
		burst = defaultKeyBurst
	}
	return &keyLimiters{rps: rate.Limit(rps), burst: burst, now: time.Now, limiters: map[uuid.UUID]*keyLimiter{}}
}

// allow takes one request from the budget of the key.
func (k *keyLimiters) allow(id uuid.UUID) bool {
	now := k.now()
	k.mu.Lock()
	defer k.mu.Unlock()
	if now.Sub(k.lastSweep) > keyLimiterTTL {
		for id, l := range k.limiters {
			if now.Sub(l.seen) > keyLimiterTTL {
				delete(k.limiters, id)
			}
		}
		k.lastSweep = now
	}
	l, ok := k.limiters[id]
	if !ok {
		l = &keyLimiter{l: rate.NewLimiter(k.rps, k.burst)}
		k.limiters[id] = l
	}
	l.seen = now
	return l.l.AllowN(now, 1)
}

// apiKeyLimiter limits the requests of each API key. It does not limit
// other token kinds, and it runs after the bearer middleware.
func (s *Server) apiKeyLimiter() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if t, ok := authctx.TokenFrom(c); ok && t.Kind == store.TokenKindAPI && s.apiKeyRates != nil && !s.apiKeyRates.allow(t.ID) {
				c.Response().Header().Set(echo.HeaderRetryAfter, "1")
				return echo.NewHTTPError(http.StatusTooManyRequests, "API key rate limit exceeded")
			}
			return next(c)
		}
	}
}
