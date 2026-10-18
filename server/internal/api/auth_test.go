package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/store"
)

// A token that a middleware sets (a cloud session) passes the bearer
// check, and the kind check still applies.
func TestPresetToken(t *testing.T) {
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	session := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if k := c.Request().Header.Get("X-Test-Kind"); k != "" {
				authctx.Set(c, store.Token{OrgID: store.ZeroOrg, Kind: store.TokenKind(k)})
			}
			return next(c)
		}
	}
	srv := New(Deps{
		Config:     &config.Config{},
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:      st,
		Issuer:     auth.NewIssuer(st),
		Middleware: []echo.MiddlewareFunc{session},
	})
	for kind, want := range map[string]int{
		"":                             http.StatusUnauthorized,
		"user":                         http.StatusOK,
		string(store.TokenKindSampler): http.StatusForbidden,
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/nodes", nil)
		req.Header.Set("X-Test-Kind", kind)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("kind %q: %d, want %d", kind, rec.Code, want)
		}
	}
}
