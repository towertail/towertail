// Package authctx gives handlers the token and the role that the bearer
// middleware verified.
package authctx

import (
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/store"
)

const (
	key     = "towertail.token"
	roleKey = "towertail.role"
)

// Set stores the verified token on the request context.
func Set(c echo.Context, t store.Token) { c.Set(key, t) }

// TokenFrom returns the verified token of the request.
func TokenFrom(c echo.Context) (store.Token, bool) {
	t, ok := c.Get(key).(store.Token)
	return t, ok
}

// SetRole stores the role of the token user in its org.
func SetRole(c echo.Context, r store.Role) { c.Set(roleKey, r) }

// RoleFrom returns the role that a middleware set.
func RoleFrom(c echo.Context) (store.Role, bool) {
	r, ok := c.Get(roleKey).(store.Role)
	return r, ok
}
