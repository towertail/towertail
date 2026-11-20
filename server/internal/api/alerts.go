package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// alertsReady answers 503 when ClickHouse is on but not ready. A server
// with no ClickHouse has no alerts, and that is not an error.
func (s *Server) alertsReady() error {
	if s.ch != nil && !s.ch.Ready() {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse not ready")
	}
	return nil
}

func (s *Server) handleListAlerts(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	if err := s.alertsReady(); err != nil {
		return err
	}
	out, err := s.query.Alerts(c.Request().Context(), query.AlertsRequest{OrgID: t.OrgID, Open: true})
	if err != nil {
		return fleetQueryError(err)
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) handleRecentAlerts(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	var days int
	if v := c.QueryParam("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return fleetQueryError(query.ErrBadDays)
		}
		days = n
	}
	if err := s.alertsReady(); err != nil {
		return err
	}
	out, err := s.query.Alerts(c.Request().Context(), query.AlertsRequest{OrgID: t.OrgID, Days: days})
	if err != nil {
		return fleetQueryError(err)
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
	return query.AlertFromRow(r), nil
}
