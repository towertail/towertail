package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/wire"
)

func (s *Server) handleListAlerts(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	out := wire.AlertList{Alerts: []wire.Alert{}}
	if s.ch == nil {
		return c.JSON(http.StatusOK, out)
	}
	if !s.ch.Ready() {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse not ready")
	}
	rows, err := s.ch.OpenAlerts(c.Request().Context(), t.OrgID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		out.Alerts = append(out.Alerts, alertFromRow(r))
	}
	return c.JSON(http.StatusOK, out)
}

// recentAlertsLimit is the most rows of GET /v1/alerts/recent.
const recentAlertsLimit = 200

func (s *Server) handleRecentAlerts(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	days := 7
	if v := c.QueryParam("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 90 {
			return echo.NewHTTPError(http.StatusBadRequest, "days must be 1 to 90")
		}
		days = n
	}
	out := wire.AlertList{Alerts: []wire.Alert{}}
	if s.ch == nil {
		return c.JSON(http.StatusOK, out)
	}
	if !s.ch.Ready() {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse not ready")
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	rows, err := s.ch.RecentAlerts(c.Request().Context(), t.OrgID, since, recentAlertsLimit)
	if err != nil {
		return err
	}
	for _, r := range rows {
		out.Alerts = append(out.Alerts, alertFromRow(r))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) handleAckAlert(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	a, err := s.openAlert(c)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := s.ch.AckAlert(c.Request().Context(), t.OrgID, a.ID, t.ID, now); err != nil {
		return err
	}
	a.AckedBy, a.AckedAt = &t.ID, &now
	s.audit(c, "ack", "alert", a.ID)
	return c.JSON(http.StatusOK, a)
}

func (s *Server) handleSnoozeAlert(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	until, err := time.Parse(time.RFC3339, c.QueryParam("until"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "until must be RFC3339")
	}
	until = until.UTC()
	a, err := s.openAlert(c)
	if err != nil {
		return err
	}
	if err := s.ch.SnoozeAlert(c.Request().Context(), t.OrgID, a.ID, until); err != nil {
		return err
	}
	a.SnoozedUntil = &until
	s.audit(c, "snooze", "alert", a.ID, "until", until)
	return c.JSON(http.StatusOK, a)
}

// openAlert finds the open alert of the path id in the org of the caller.
func (s *Server) openAlert(c echo.Context) (wire.Alert, error) {
	t, _ := authctx.TokenFrom(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return wire.Alert{}, echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	if s.ch == nil {
		return wire.Alert{}, echo.NewHTTPError(http.StatusNotFound, "alert not found")
	}
	if !s.ch.Ready() {
		return wire.Alert{}, echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse not ready")
	}
	r, err := s.ch.Alert(c.Request().Context(), t.OrgID, id)
	if errors.Is(err, clickhouse.ErrNoAlert) {
		return wire.Alert{}, echo.NewHTTPError(http.StatusNotFound, "alert not found")
	}
	if err != nil {
		return wire.Alert{}, err
	}
	return alertFromRow(r), nil
}

func alertFromRow(r clickhouse.AlertRow) wire.Alert {
	var p struct {
		Value float64 `json:"value"`
	}
	_ = json.Unmarshal([]byte(r.Payload), &p)
	return wire.Alert{
		ID:           r.EventID,
		NodeID:       r.NodeID,
		Kind:         r.Kind,
		Metric:       r.Metric,
		Tint:         wire.Tint(r.Tint),
		TS:           r.TS,
		Value:        p.Value,
		AckedBy:      r.AckedBy,
		AckedAt:      r.AckedAt,
		SnoozedUntil: r.SnoozedUntil,
	}
}
