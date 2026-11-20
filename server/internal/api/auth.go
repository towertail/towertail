package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// Roles gives the role of a user in an org.
type Roles interface {
	Role(ctx context.Context, orgID, userID uuid.UUID) (store.Role, error)
}

// bearer rejects requests without a valid bearer token.
// kinds may contain multiple kinds (e.g. user + admin); empty means any.
// A token that an earlier middleware set with authctx.Set (for example a
// cloud session cookie) counts as verified. After the check, bearer sets
// the role of user, admin and api tokens, unless an earlier middleware
// did. A user or admin token with no user, or a server with no Roles,
// gives owner. An api token always gives viewer, and it needs a membership
// when it has a user.
func (s *Server) bearer(kinds ...store.TokenKind) echo.MiddlewareFunc {
	allowed := map[store.TokenKind]struct{}{}
	for _, k := range kinds {
		allowed[k] = struct{}{}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			t, ok := authctx.TokenFrom(c)
			if !ok {
				raw := extractBearer(c)
				if raw == "" {
					return echo.NewHTTPError(http.StatusUnauthorized, "missing token")
				}
				var err error
				if t, err = s.issuer.Verify(ctx, raw); err != nil {
					return echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
				}
			}
			if len(allowed) > 0 {
				if _, ok := allowed[t.Kind]; !ok {
					return echo.NewHTTPError(http.StatusForbidden, "token kind not allowed")
				}
			}
			authctx.Set(c, t)
			if _, has := authctx.RoleFrom(c); has {
				return next(c)
			}
			var role store.Role
			switch t.Kind {
			case store.TokenKindUser, store.TokenKindAdmin:
				role = store.RoleOwner
			case store.TokenKindAPI:
				role = store.RoleViewer
			default:
				return next(c)
			}
			if s.roles != nil && t.UserID != nil {
				member, err := s.roles.Role(ctx, t.OrgID, *t.UserID)
				if errors.Is(err, store.ErrNoMembership) {
					return echo.NewHTTPError(http.StatusForbidden, "no access to this org")
				} else if err != nil {
					return err
				}
				if t.Kind != store.TokenKindAPI {
					role = member
				}
			}
			authctx.SetRole(c, role)
			return next(c)
		}
	}
}

// RequireRole rejects a request when the role on the context is below
// need, or when there is no role.
func RequireRole(need store.Role) echo.MiddlewareFunc { return requireRole(need) }

func requireRole(need store.Role) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if r, ok := authctx.RoleFrom(c); !ok || !r.Allows(need) {
				return echo.NewHTTPError(http.StatusForbidden, fmt.Sprintf("role %s needed", need))
			}
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

// handleLogin authenticates an email+password and returns a user token.
func (s *Server) handleLogin(c echo.Context) error {
	var req wire.LoginRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := c.Validate(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	u, err := s.store.GetUserByEmail(c.Request().Context(), strings.ToLower(strings.TrimSpace(req.Email)))
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
	}
	ok, err := auth.VerifyPassword(req.Password, u.PasswordHash)
	if err != nil || !ok {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
	}
	raw, _, err := s.issuer.Issue(c.Request().Context(), u.OrgID, store.TokenKindUser, "login", nil, &u.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, wire.LoginResponse{Token: raw})
}

// handleLogout revokes the caller's token.
func (s *Server) handleLogout(c echo.Context) error {
	t, ok := authctx.TokenFrom(c)
	if !ok {
		return echo.NewHTTPError(http.StatusUnauthorized, "")
	}
	if err := s.issuer.Revoke(c.Request().Context(), t.ID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
