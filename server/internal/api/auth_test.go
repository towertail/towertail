package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
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

type fakeRoles struct {
	role store.Role
	err  error
}

func (f fakeRoles) Role(context.Context, uuid.UUID, uuid.UUID) (store.Role, error) {
	return f.role, f.err
}

// newRoleServer builds a server whose middleware sets the token that
// the X-Test-* headers describe.
func newRoleServer(t *testing.T, roles Roles) *Server {
	t.Helper()
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	session := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			h := c.Request().Header
			if k := h.Get("X-Test-Kind"); k != "" {
				tok := store.Token{OrgID: store.ZeroOrg, Kind: store.TokenKind(k)}
				if h.Get("X-Test-User") != "" {
					id := uuid.New()
					tok.UserID = &id
				}
				authctx.Set(c, tok)
			}
			if r := h.Get("X-Test-Role"); r != "" {
				authctx.SetRole(c, store.Role(r))
			}
			return next(c)
		}
	}
	return New(Deps{
		Config:     &config.Config{},
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:      st,
		Issuer:     auth.NewIssuer(st),
		Roles:      roles,
		Channels:   &fakeChannels{},
		Middleware: []echo.MiddlewareFunc{session},
	})
}

func (s *Server) call(method, path string, h map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range h {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestBearerRole(t *testing.T) {
	user := map[string]string{"X-Test-Kind": "user"}
	withUser := map[string]string{"X-Test-Kind": "user", "X-Test-User": "1"}
	preset := map[string]string{"X-Test-Kind": "user", "X-Test-User": "1", "X-Test-Role": "viewer"}
	sampler := map[string]string{"X-Test-Kind": "sampler"}

	noRoles := newRoleServer(t, nil)
	if rec := noRoles.call(http.MethodPut, "/v1/settings", user); rec.Code == http.StatusForbidden {
		t.Errorf("nil roles: %d, want owner", rec.Code)
	}
	if rec := noRoles.call(http.MethodPut, "/v1/settings", withUser); rec.Code == http.StatusForbidden {
		t.Errorf("nil roles with user: %d, want owner", rec.Code)
	}

	viewer := newRoleServer(t, fakeRoles{role: store.RoleViewer})
	if rec := viewer.call(http.MethodPut, "/v1/settings", user); rec.Code == http.StatusForbidden {
		t.Errorf("token with no user: %d, want owner", rec.Code)
	}
	if rec := viewer.call(http.MethodPut, "/v1/settings", withUser); rec.Code != http.StatusForbidden {
		t.Errorf("viewer on admin route: %d, want 403", rec.Code)
	}
	if rec := viewer.call(http.MethodGet, "/v1/nodes", withUser); rec.Code != http.StatusOK {
		t.Errorf("viewer on read route: %d, want 200", rec.Code)
	}

	none := newRoleServer(t, fakeRoles{err: store.ErrNoMembership})
	rec := none.call(http.MethodGet, "/v1/nodes", withUser)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "no access to this org") {
		t.Errorf("no membership: %d %s", rec.Code, rec.Body)
	}
	broken := newRoleServer(t, fakeRoles{err: errors.New("db down")})
	if rec := broken.call(http.MethodGet, "/v1/nodes", withUser); rec.Code != http.StatusInternalServerError {
		t.Errorf("source error: %d, want 500", rec.Code)
	}

	// A role from an earlier middleware stays.
	owner := newRoleServer(t, fakeRoles{role: store.RoleViewer})
	preset["X-Test-Role"] = "owner"
	if rec := owner.call(http.MethodPut, "/v1/settings", preset); rec.Code == http.StatusForbidden {
		t.Errorf("preset owner: %d, want not 403", rec.Code)
	}
	preset["X-Test-Role"] = "viewer"
	noRoles = newRoleServer(t, nil)
	if rec := noRoles.call(http.MethodPut, "/v1/settings", preset); rec.Code != http.StatusForbidden {
		t.Errorf("preset viewer: %d, want 403", rec.Code)
	}

	// A sampler token gets no role, so a role route is out of reach.
	var got store.Role
	var has bool
	probe := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			err := next(c)
			got, has = authctx.RoleFrom(c)
			return err
		}
	}
	srv := newRoleServer(t, nil)
	srv.echo.Group("/probe", probe, srv.bearer(store.TokenKindSampler)).GET("", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	if rec := srv.call(http.MethodGet, "/probe", sampler); rec.Code != http.StatusOK || has {
		t.Errorf("sampler: %d, role %q %v, want no role", rec.Code, got, has)
	}
}

func TestSelfHostedUnchanged(t *testing.T) {
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	hash, err := auth.HashPassword("pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutUser(context.Background(), store.User{ID: uuid.New(), OrgID: store.ZeroOrg, Email: "a@b.c", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{
		Config: &config.Config{},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:  st,
		Issuer: auth.NewIssuer(st),
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(`{"email":"a@b.c","password":"pw"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var login wire.LoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil || login.Token == "" {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	h := map[string]string{"Authorization": "Bearer " + login.Token}
	if rec := srv.call(http.MethodPut, "/v1/settings", h); rec.Code != http.StatusOK {
		t.Errorf("PUT settings: %d %s", rec.Code, rec.Body)
	}
	if rec := srv.call(http.MethodDelete, "/v1/nodes/"+uuid.NewString(), h); rec.Code == http.StatusForbidden {
		t.Errorf("DELETE node: %d", rec.Code)
	}
}
