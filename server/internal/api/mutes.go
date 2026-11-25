package api

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/actions"
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
		return actionError(actions.ErrBadMetric)
	}
	if b.NodeID == nil {
		if r, ok := authctx.RoleFrom(c); !ok || !r.Allows(store.RoleAdmin) {
			return echo.NewHTTPError(http.StatusForbidden, "role admin needed")
		}
	}
	res, err := s.actions.SetMute(c.Request().Context(), actions.NewActor(t, "", ""), b.Metric, b.NodeID, b.Muted)
	if err != nil {
		return actionError(err)
	}
	if res.Node != nil {
		s.audit(c, "mute", "node", res.Node.ID, "metric", b.Metric, "muted", b.Muted)
		return c.JSON(http.StatusOK, res.Node)
	}
	s.audit(c, "mute", "settings", uuid.Nil, "metric", b.Metric, "muted", b.Muted)
	return c.JSON(http.StatusOK, res.Settings)
}
