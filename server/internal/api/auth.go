package api

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/towertail/server/internal/auth"
	"github.com/towertail/server/internal/store"
	"github.com/towertail/server/internal/wire"
)

// tokenKey is the Echo context key used to store the authenticated
// token struct for handlers downstream.
const tokenKey = "towertail.token"

// bearerMiddleware rejects requests without a valid bearer token.
// kinds may contain multiple kinds (e.g. user + admin); empty means any.
func bearerMiddleware(issuer *auth.TokenIssuer, kinds ...store.TokenKind) echo.MiddlewareFunc {
	allowed := map[store.TokenKind]struct{}{}
	for _, k := range kinds {
		allowed[k] = struct{}{}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			raw := extractBearer(c)
			if raw == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing token")
			}
			t, err := issuer.Verify(c.Request().Context(), raw)
			if err != nil {
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
			}
			if len(allowed) > 0 {
				if _, ok := allowed[t.Kind]; !ok {
					return echo.NewHTTPError(http.StatusForbidden, "token kind not allowed")
				}
			}
			c.Set(tokenKey, t)
			return next(c)
		}
	}
}

func extractBearer(c echo.Context) string {
	h := c.Request().Header.Get(echo.HeaderAuthorization)
	if h == "" {
		return ""
	}
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[len("Bearer "):])
}

func tokenFromCtx(c echo.Context) (store.Token, bool) {
	v := c.Get(tokenKey)
	if v == nil {
		return store.Token{}, false
	}
	t, ok := v.(store.Token)
	return t, ok
}

// handleLogin authenticates an email+password and returns a user token.
func (s *Server) handleLogin(c echo.Context) error {
	var req wire.LoginRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := c.Validate(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	u, err := s.store.GetUserByEmail(c.Request().Context(), req.Email)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
	}
	ok, err := auth.VerifyPassword(req.Password, u.PasswordHash)
	if err != nil || !ok {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
	}
	raw, _, err := s.issuer.Issue(c.Request().Context(), u.OrgID, store.TokenKindUser, "login", nil)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, wire.LoginResponse{Token: raw})
}

// handleLogout revokes the caller's token.
func (s *Server) handleLogout(c echo.Context) error {
	t, ok := tokenFromCtx(c)
	if !ok {
		return echo.NewHTTPError(http.StatusUnauthorized, "")
	}
	if err := s.issuer.Revoke(c.Request().Context(), t.ID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
