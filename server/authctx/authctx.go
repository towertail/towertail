// Package authctx gives handlers the token that the bearer middleware
// verified.
package authctx

import (
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/store"
)

const key = "towertail.token"

// Set stores the verified token on the request context.
func Set(c echo.Context, t store.Token) { c.Set(key, t) }

// TokenFrom returns the verified token of the request.
func TokenFrom(c echo.Context) (store.Token, bool) {
	t, ok := c.Get(key).(store.Token)
	return t, ok
}
