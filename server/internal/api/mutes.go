package api

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/store"
)

type muteBody struct {
	Metric store.ThresholdMetric `json:"metric"`
	NodeID *uuid.UUID            `json:"nodeId,omitempty"`
	Muted  bool                  `json:"muted"`
}

// handlePutMute sets or clears the mute of one metric on the org (admin)
// or on one host (member). The engine closes a muted open alert.
func (s *Server) handlePutMute(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	var b muteBody
	if err := c.Bind(&b); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if !b.Metric.Valid() {
		return echo.NewHTTPError(http.StatusBadRequest, "unknown metric")
	}
	ctx := c.Request().Context()
	if b.NodeID != nil {
		n, err := s.store.EditNode(ctx, t.OrgID, *b.NodeID, func(n *store.Node) error {
			n.MutedSignals = n.MutedSignals.Set(b.Metric, b.Muted)
			return nil
		})
		if err != nil {
			return err
		}
		s.audit(c, "mute", "node", n.ID, "metric", b.Metric, "muted", b.Muted)
		s.broadcastNode(t.OrgID, n)
		return c.JSON(http.StatusOK, n)
	}
	if r, ok := authctx.RoleFrom(c); !ok || !r.Allows(store.RoleAdmin) {
		return echo.NewHTTPError(http.StatusForbidden, "role admin needed")
	}
	cfg, err := s.store.EditSettings(ctx, t.OrgID, func(cfg *store.ServerSettings) error {
		cfg.Muted = cfg.Muted.Set(b.Metric, b.Muted)
		return nil
	})
	if err != nil {
		return err
	}
	if s.alerter != nil {
		s.alerter.InvalidateSettings(t.OrgID)
	}
	s.audit(c, "mute", "settings", uuid.Nil, "metric", b.Metric, "muted", b.Muted)
	s.broadcastSettings(t.OrgID, cfg)
	return c.JSON(http.StatusOK, cfg)
}
