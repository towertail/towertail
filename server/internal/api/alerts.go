package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
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

// recentSince reads days and gives the start of the recent alerts. The
// plan limit clamps it.
func (s *Server) recentSince(c echo.Context, now time.Time) (since time.Time, clamped bool, err error) {
	t, _ := authctx.TokenFrom(c)
	days := 7
	if v := c.QueryParam("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 90 {
			return time.Time{}, false, echo.NewHTTPError(http.StatusBadRequest, "days must be 1 to 90")
		}
		days = n
	}
	since = now.Add(-time.Duration(days) * 24 * time.Hour)
	since, clamped = s.query.Clamp(c.Request().Context(), t.OrgID, since, now)
	return since, clamped, nil
}

func (s *Server) handleRecentAlerts(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	since, clamped, err := s.recentSince(c, time.Now())
	if err != nil {
		return err
	}
	out := wire.AlertList{Alerts: []wire.Alert{}, Clamped: clamped}
	if s.ch == nil {
		return c.JSON(http.StatusOK, out)
	}
	if !s.ch.Ready() {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse not ready")
	}
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
	by := ackedBy(t)
	ctx := c.Request().Context()
	if err := s.ch.AckAlert(ctx, t.OrgID, a.ID, by, now); err != nil {
		return err
	}
	a.AckedBy, a.AckedAt = &by, &now
	s.audit(c, "ack", "alert", a.ID)
	s.tellAcked(ctx, t, a, by, now, false)
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
	s.tellAcked(c.Request().Context(), t, a, ackedBy(t), time.Now().UTC(), true)
	return c.JSON(http.StatusOK, a)
}

// ackedBy is the user of the token when it has one, else the token.
func ackedBy(t store.Token) uuid.UUID {
	if t.UserID != nil {
		return *t.UserID
	}
	return t.ID
}

func (s *Server) tellAcked(ctx context.Context, t store.Token, a wire.Alert, by uuid.UUID, at time.Time, snooze bool) {
	if s.alerter == nil {
		return
	}
	s.alerter.Acked(ctx, notify.Ack{OrgID: t.OrgID, EventID: a.ID, NodeID: a.NodeID, Metric: a.Metric, By: by, At: at, Snooze: snooze})
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
