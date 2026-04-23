package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/server/internal/store"
	"github.com/towertail/server/internal/wire"
)

func (s *Server) handleGetSettings(c echo.Context) error {
	t, _ := tokenFromCtx(c)
	cfg, err := s.store.GetSettings(c.Request().Context(), t.OrgID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, cfg)
}

func (s *Server) handlePutSettings(c echo.Context) error {
	t, _ := tokenFromCtx(c)
	var cfg store.ServerSettings
	if err := c.Bind(&cfg); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := s.store.PutSettings(c.Request().Context(), t.OrgID, cfg); err != nil {
		return err
	}
	s.audit(c, "update", "settings", uuid.Nil)
	if s.alerter != nil {
		s.alerter.UpdateSettings(cfg)
	}
	if s.hub != nil {
		if raw, err := json.Marshal(cfg); err == nil {
			s.hub.BroadcastOrg(t.OrgID, wire.WSMessage{Type: "settings_updated", Setting: raw})
		}
	}
	return c.JSON(http.StatusOK, cfg)
}
