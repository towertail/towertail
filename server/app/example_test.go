package app_test

import (
	"context"
	"log"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/app"
	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/store"
)

// Example shows how another module adds its own routes to the server.
func Example() {
	cfg, err := config.Load("")
	if err != nil {
		log.Fatal(err)
	}
	a, err := app.New(*cfg,
		app.WithRoutes(func(r app.Router) {
			g := r.Group("/v1/account", store.TokenKindUser)
			g.GET("", func(c echo.Context) error {
				t, _ := authctx.TokenFrom(c)
				return c.JSON(http.StatusOK, map[string]any{"org_id": t.OrgID})
			})
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
	_ = a.Run(context.Background())
}
