package api

import (
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/actions"
)

func (s *Server) handleGetSettings(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	cfg, err := s.store.GetSettings(c.Request().Context(), t.OrgID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, cfg)
}

// handlePutSettings decodes the body onto the stored settings, so a key
// that the body leaves out keeps its stored value. When the body changes
// a rule, the server sets the origin of the rules.
func (s *Server) handlePutSettings(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	cfg, _, err := s.actions.UpdateSettings(c.Request().Context(), actions.NewActor(t, "", ""), body)
	if err != nil {
		return actionError(err)
	}
	s.audit(c, "update", "settings", uuid.Nil)
	return c.JSON(http.StatusOK, cfg)
}
