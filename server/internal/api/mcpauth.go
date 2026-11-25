package api

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/store"
)

const protectedResourcePath = "/.well-known/oauth-protected-resource"

// mcpChallenge adds the WWW-Authenticate header to each 401 of the MCP
// routes.
func (s *Server) mcpChallenge() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			err := next(c)
			var he *echo.HTTPError
			if !errors.As(err, &he) || he.Code != http.StatusUnauthorized {
				return err
			}
			c.Response().Header().Set(echo.HeaderWWWAuthenticate, s.mcpChallengeValue(c))
			return err
		}
	}
}

func (s *Server) mcpChallengeValue(c echo.Context) string {
	if !s.cfg.MCP.OAuth() {
		return `Bearer realm="towertail"`
	}
	u, err := url.Parse(s.cfg.MCP.ResourceURL)
	if err != nil {
		return `Bearer realm="towertail"`
	}
	meta := u.Scheme + "://" + u.Host + protectedResourcePath + c.Request().URL.Path
	v := `Bearer resource_metadata="` + meta + `", scope="` + string(store.ScopeFleetRead) + `"`
	if extractBearer(c) != "" {
		v += `, error="invalid_token"`
	}
	return v
}

// mcpAudience checks the audience of an oauth token. Both MCP paths accept
// each audience.
func (s *Server) mcpAudience() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if t, ok := authctx.TokenFrom(c); ok && t.Kind == store.TokenKindOAuth {
				if !s.cfg.MCP.OAuth() || t.Audience == "" || !slices.Contains(s.cfg.MCP.Resources(), t.Audience) {
					return echo.NewHTTPError(http.StatusUnauthorized, "token audience does not match")
				}
			}
			return next(c)
		}
	}
}

// mcpAPIKeyGate stops the API keys of an org that turned them off for MCP.
func (s *Server) mcpAPIKeyGate() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			t, ok := authctx.TokenFrom(c)
			if !ok || t.Kind != store.TokenKindAPI {
				return next(c)
			}
			cfg, err := s.store.GetSettings(c.Request().Context(), t.OrgID)
			if err != nil {
				return err
			}
			if !cfg.MCP.APIKeys {
				return echo.NewHTTPError(http.StatusForbidden, "API keys are off for MCP in this org")
			}
			return next(c)
		}
	}
}

// handleProtectedResource serves the RFC 9728 metadata of the MCP path of
// the request.
func (s *Server) handleProtectedResource(c echo.Context) error {
	resource := s.cfg.MCP.ResourceURL
	if strings.HasSuffix(c.Request().URL.Path, "/v1") {
		resource += "/v1"
	}
	return c.JSON(http.StatusOK, oauthex.ProtectedResourceMetadata{
		Resource:               resource,
		AuthorizationServers:   []string{s.cfg.MCP.AuthorizationServer},
		ScopesSupported:        []string{string(store.ScopeFleetRead), string(store.ScopeAlertsWrite), string(store.ScopeRulesWrite)},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "Towertail",
		ResourceDocumentation:  s.cfg.MCP.DocsURL,
	})
}

// publicCORS lets browser clients of any site call the route. It allows no
// credentials, because a bearer token authenticates the call.
func publicCORS() echo.MiddlewareFunc {
	return middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins:  []string{"*"},
		AllowMethods:  []string{http.MethodGet, http.MethodPost, http.MethodOptions},
		AllowHeaders:  []string{echo.HeaderAuthorization, echo.HeaderContentType, "Mcp-Protocol-Version", "Mcp-Session-Id"},
		ExposeHeaders: []string{echo.HeaderWWWAuthenticate},
	})
}
