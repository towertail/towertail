package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
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

// alertReader is the part of ClickHouse that the alert routes read.
type alertReader interface {
	Ready() bool
	Alert(ctx context.Context, orgID, id uuid.UUID) (clickhouse.AlertRow, error)
	AlertMarks(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]clickhouse.AlertMark, error)
}

// alertsReady answers 503 when ClickHouse is on but not ready. A server
// with no ClickHouse has no alerts, and that is not an error.
func (s *Server) alertsReady() error {
	if s.alerts != nil && !s.alerts.Ready() {
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
	return s.markAlert(c, notify.LineAcked)
}

func (s *Server) handleUnackAlert(c echo.Context) error {
	return s.markAlert(c, notify.LineUnacked)
}

func (s *Server) handleSnoozeAlert(c echo.Context) error {
	return s.markAlert(c, notify.LineSnoozed)
}

func (s *Server) handleUnsnoozeAlert(c echo.Context) error {
	return s.markAlert(c, notify.LineUnsnoozed)
}

// markAlert sets or takes back the ack or the snooze of an open alert.
func (s *Server) markAlert(c echo.Context, kind notify.LineKind) error {
	t, _ := authctx.TokenFrom(c)
	now := time.Now().UTC()
	var until *time.Time
	if kind == notify.LineSnoozed {
		u, err := parseFuture(c.QueryParam("until"), now)
		if err != nil {
			return err
		}
		until = &u
	}
	a, err := s.openAlert(c, true)
	if err != nil {
		return err
	}
	by := ackedBy(t)
	ack := notify.Ack{OrgID: t.OrgID, EventID: a.ID, AlertID: a.ID, NodeID: a.NodeID, Metric: a.Metric, By: by, At: now}
	switch kind {
	case notify.LineAcked:
		a.AckedBy, a.AckedAt = &by, &now
	case notify.LineUnacked:
		a.AckedBy, a.AckedAt = nil, nil
		ack.Undo = true
	case notify.LineSnoozed:
		a.SnoozedUntil = until
		ack.Snooze, ack.Until = true, until
	case notify.LineUnsnoozed:
		a.SnoozedUntil = nil
		ack.Snooze, ack.Undo = true, true
	}
	ctx := c.Request().Context()
	s.writeJournal(ctx, notify.Line{OrgID: t.OrgID, NodeID: a.NodeID, AlertID: a.ID, At: now, Kind: kind, User: &by, Until: until})
	s.audit(c, string(kind), "alert", a.ID)
	s.tellAcked(ctx, a, ack)
	s.broadcastAlert(t.OrgID, a)
	return c.JSON(http.StatusOK, a)
}

// parseFuture reads an RFC3339 time that is after now.
func parseFuture(v string, now time.Time) (time.Time, error) {
	u, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, echo.NewHTTPError(http.StatusBadRequest, "until must be RFC3339")
	}
	if !u.After(now) {
		return time.Time{}, echo.NewHTTPError(http.StatusBadRequest, "until must be in the future")
	}
	return u.UTC(), nil
}

func (s *Server) handleIgnoreAlert(c echo.Context) error   { return s.ignoreAlert(c, true) }
func (s *Server) handleUnignoreAlert(c echo.Context) error { return s.ignoreAlert(c, false) }

// ignoreAlert adds the process name of a long_process alert to the ignore
// list of the host or of the org, or removes it. The engine closes the
// open alert with no notification.
func (s *Server) ignoreAlert(c echo.Context, add bool) error {
	t, _ := authctx.TokenFrom(c)
	scope := c.QueryParam("scope")
	if scope != "host" && scope != "all" {
		return echo.NewHTTPError(http.StatusBadRequest, "scope must be host or all")
	}
	a, err := s.openAlert(c, false)
	if err != nil {
		return err
	}
	if a.Kind != "long_process" || a.Proc == nil || a.Proc.Name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "only a long_process alert has a process to ignore")
	}
	name := a.Proc.Name
	ctx := c.Request().Context()
	edit := func(l []string) []string {
		l = slices.DeleteFunc(slices.Clone(l), func(x string) bool { return strings.EqualFold(x, name) })
		if add {
			l = append(l, name)
		}
		return l
	}
	var text string
	if scope == "host" {
		n, err := s.store.EditNode(ctx, t.OrgID, a.NodeID, func(n *store.Node) error {
			n.IgnoredProcs = edit(n.IgnoredProcs)
			return nil
		})
		if err != nil {
			return err
		}
		s.broadcastNode(t.OrgID, n)
		text = name + " ignored on " + n.DisplayName
	} else {
		cfg, err := s.store.EditSettings(ctx, t.OrgID, func(cfg *store.ServerSettings) error {
			cfg.LongProc.Ignore = edit(cfg.LongProc.Ignore)
			return nil
		})
		if err != nil {
			return err
		}
		if s.alerter != nil {
			s.alerter.InvalidateSettings(t.OrgID)
		}
		s.broadcastSettings(t.OrgID, cfg)
		text = name + " ignored on all hosts"
	}
	by := ackedBy(t)
	if add {
		s.writeJournal(ctx, notify.Line{OrgID: t.OrgID, NodeID: a.NodeID, AlertID: a.ID, At: time.Now().UTC(), Kind: notify.LineClosed, User: &by, Text: text})
	}
	action := "unignore"
	if add {
		action = "ignore"
	}
	s.audit(c, action, "alert", a.ID, "name", name, "scope", scope)
	s.broadcastAlert(t.OrgID, a)
	return c.JSON(http.StatusOK, a)
}

// ackedBy is the user of the token when it has one, else the token.
func ackedBy(t store.Token) uuid.UUID {
	if t.UserID != nil {
		return *t.UserID
	}
	return t.ID
}

func (s *Server) tellAcked(ctx context.Context, a wire.Alert, ack notify.Ack) {
	if s.alerter == nil {
		return
	}
	s.alerter.Acked(ctx, ack)
}

func (s *Server) writeJournal(ctx context.Context, lines ...notify.Line) {
	if s.journal == nil {
		return
	}
	if err := s.journal.Write(ctx, lines...); err != nil {
		s.log.Warn("alert log", "err", err)
	}
}

func (s *Server) broadcastAlert(orgID uuid.UUID, a wire.Alert) {
	if s.hub == nil {
		return
	}
	s.hub.BroadcastOrg(orgID, wire.WSMessage{Type: "alert_updated", NodeID: &a.NodeID, Alert: &a})
}

// openAlert finds the alert of the path id in the org of the caller. With
// open set, an alert that is closed gives 404.
func (s *Server) openAlert(c echo.Context, open bool) (wire.Alert, error) {
	t, _ := authctx.TokenFrom(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return wire.Alert{}, echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	if s.alerts == nil {
		return wire.Alert{}, echo.NewHTTPError(http.StatusNotFound, "alert not found")
	}
	if !s.alerts.Ready() {
		return wire.Alert{}, echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse not ready")
	}
	ctx := c.Request().Context()
	r, err := s.alerts.Alert(ctx, t.OrgID, id)
	if errors.Is(err, clickhouse.ErrNoAlert) || (err == nil && open && r.Tint == "ok") {
		return wire.Alert{}, echo.NewHTTPError(http.StatusNotFound, "alert not found")
	}
	if err != nil {
		return wire.Alert{}, err
	}
	aid := id
	if r.AlertID != uuid.Nil {
		aid = r.AlertID
	}
	marks, err := s.alerts.AlertMarks(ctx, t.OrgID, []uuid.UUID{aid})
	if err != nil {
		return wire.Alert{}, err
	}
	return query.AlertFromRow(r, marks[aid]), nil
}
