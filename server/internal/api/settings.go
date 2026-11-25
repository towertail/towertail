package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
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
	var in struct {
		RulesOrigin *struct {
			Client string `json:"client"`
			Device string `json:"device"`
		} `json:"rulesOrigin"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	cfg, err := s.store.EditSettings(c.Request().Context(), t.OrgID, func(cfg *store.ServerSettings) error {
		var oldOrigin *store.RulesOrigin // a copy: the decode writes through the pointer
		if cfg.RulesOrigin != nil {
			o := *cfg.RulesOrigin
			oldOrigin = &o
		}
		before := ruleKey(*cfg)
		if err := json.Unmarshal(body, cfg); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := cfg.Rules().Validate(); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := cfg.MCP.Validate(); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		cfg.RulesOrigin = oldOrigin
		if ruleKey(*cfg) != before {
			var client, device string
			if in.RulesOrigin != nil {
				client, device = in.RulesOrigin.Client, in.RulesOrigin.Device
			}
			o := newRulesOrigin(t, client, device)
			cfg.RulesOrigin = &o
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.audit(c, "update", "settings", uuid.Nil)
	if s.alerter != nil {
		s.alerter.InvalidateSettings(t.OrgID)
	}
	s.broadcastSettings(t.OrgID, cfg)
	return c.JSON(http.StatusOK, cfg)
}

// ruleKey is the JSON of the org rules without the origin. Two equal keys
// mean that the rules did not change.
func ruleKey(cfg store.ServerSettings) string {
	r := cfg.Rules()
	r.Origin = nil
	b, _ := json.Marshal(r)
	return string(b)
}

func (s *Server) broadcastSettings(orgID uuid.UUID, cfg store.ServerSettings) {
	if s.hub == nil {
		return
	}
	if raw, err := json.Marshal(cfg); err == nil {
		s.hub.BroadcastOrg(orgID, wire.WSMessage{Type: "settings_updated", Setting: raw})
	}
}
