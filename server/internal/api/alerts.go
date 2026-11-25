package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/actions"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// alertsReady answers 503 when ClickHouse is on but not ready. A server
// with no ClickHouse has no alerts, and that is not an error.
func (s *Server) alertsReady() error {
	if s.alerts != nil && !s.alerts.Ready() {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse not ready")
	}
	return nil
}

// handleListAlerts gives the open alerts. With a state parameter it gives
// the alerts page: the alerts of a range with counts, facets and bins.
func (s *Server) handleListAlerts(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	if err := s.alertsReady(); err != nil {
		return err
	}
	ctx := c.Request().Context()
	if !c.QueryParams().Has("state") {
		out, err := s.query.Alerts(ctx, query.AlertsRequest{OrgID: t.OrgID, Open: true})
		if err != nil {
			return fleetQueryError(err)
		}
		return c.JSON(http.StatusOK, out)
	}
	q, err := parseAlertQuery(c, t.OrgID, time.Now().UTC())
	if err != nil {
		return err
	}
	out, err := s.query.ListAlerts(ctx, q)
	if errors.Is(err, query.ErrBadAlertQuery) {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err != nil {
		return fleetQueryError(err)
	}
	return c.JSON(http.StatusOK, out)
}

// parseAlertQuery reads the parameters of the alerts page. The lists are
// comma separated. from and to use the forms of the history routes. The
// range is the last 7 days by default.
func parseAlertQuery(c echo.Context, org uuid.UUID, now time.Time) (query.AlertQuery, error) {
	p := c.QueryParam
	q := query.AlertQuery{
		OrgID:  org,
		State:  p("state"),
		Tints:  splitList(p("tint")),
		Kinds:  splitList(p("kind")),
		Q:      p("q"),
		Facets: splitList(p("facets")),
		Sort:   p("sort"),
		Limit:  query.DefaultAlertLimit,
		Now:    now,
	}
	for _, h := range splitList(p("host")) {
		id, err := uuid.Parse(h)
		if err != nil {
			return q, echo.NewHTTPError(http.StatusBadRequest, "bad host")
		}
		q.Hosts = append(q.Hosts, id)
	}
	q.From = parseRangeBound(p("from"), now.Add(-7*24*time.Hour))
	q.To = parseRangeBound(p("to"), now)
	for name, dst := range map[string]**time.Time{"started_from": &q.StartedFrom, "started_to": &q.StartedTo} {
		if v := p(name); v != "" {
			at, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return q, echo.NewHTTPError(http.StatusBadRequest, "bad "+name)
			}
			at = at.UTC()
			*dst = &at
		}
	}
	for name, dst := range map[string]*int{"bins": &q.Bins, "limit": &q.Limit} {
		if v := p(name); v != "" {
			var err error
			if *dst, err = strconv.Atoi(v); err != nil {
				return q, echo.NewHTTPError(http.StatusBadRequest, "bad "+name)
			}
		}
	}
	switch p("dir") {
	case "", "desc":
		q.Desc = true
	case "asc":
	default:
		return q, echo.NewHTTPError(http.StatusBadRequest, "bad dir")
	}
	return q, nil
}

// splitList splits a comma separated query value and drops empty items.
func splitList(v string) []string {
	var out []string
	for _, f := range strings.Split(v, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// handleGetAlert gives one alert with its limits and its timeline.
func (s *Server) handleGetAlert(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	if err := s.alertsReady(); err != nil {
		return err
	}
	out, err := s.query.AlertDetail(c.Request().Context(), t.OrgID, id)
	if errors.Is(err, clickhouse.ErrNoAlert) {
		return echo.NewHTTPError(http.StatusNotFound, "alert not found")
	}
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
	return s.markAlert(c, actions.AlertAction{Kind: actions.ActAck})
}

func (s *Server) handleUnackAlert(c echo.Context) error {
	return s.markAlert(c, actions.AlertAction{Kind: actions.ActAck, Undo: true})
}

func (s *Server) handleSnoozeAlert(c echo.Context) error {
	return s.markAlert(c, actions.AlertAction{Kind: actions.ActSnooze})
}

func (s *Server) handleUnsnoozeAlert(c echo.Context) error {
	return s.markAlert(c, actions.AlertAction{Kind: actions.ActSnooze, Undo: true})
}

// markAlert sets or takes back the ack or the snooze of an open alert.
func (s *Server) markAlert(c echo.Context, act actions.AlertAction) error {
	t, _ := authctx.TokenFrom(c)
	if act.Kind == actions.ActSnooze && !act.Undo {
		u, err := parseFuture(c.QueryParam("until"), time.Now().UTC())
		if err != nil {
			return err
		}
		act.Until = &u
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	a, err := s.actions.MarkAlert(c.Request().Context(), actions.NewActor(t, "", ""), id, act)
	if err != nil {
		return actionError(err)
	}
	s.audit(c, string(act.LineKind()), "alert", a.ID)
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

func (s *Server) handleIgnoreAlert(c echo.Context) error   { return s.ignoreAlert(c, false) }
func (s *Server) handleUnignoreAlert(c echo.Context) error { return s.ignoreAlert(c, true) }

// ignoreAlert adds the process name of a long_process alert to the ignore
// list of the host or of the org, or removes it. The engine closes the
// open alert with no notification.
func (s *Server) ignoreAlert(c echo.Context, undo bool) error {
	t, _ := authctx.TokenFrom(c)
	scope := actions.IgnoreScope(c.QueryParam("scope"))
	if !scope.Valid() {
		return echo.NewHTTPError(http.StatusBadRequest, "scope must be host or all")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	a, _, err := s.actions.IgnoreAlert(c.Request().Context(), actions.NewActor(t, "", ""), id, scope, undo)
	if err != nil {
		return actionError(err)
	}
	action := "ignore"
	if undo {
		action = "unignore"
	}
	s.audit(c, action, "alert", a.ID, "name", a.Proc.Name, "scope", string(scope))
	return c.JSON(http.StatusOK, a)
}

// MaxBulkAlerts is the most ids of one bulk request.
const MaxBulkAlerts = 200

// bulkRequest is the body of POST /v1/alerts/bulk. Action is ack, unack,
// snooze, unsnooze, ignore or unignore. Until is for snooze. Scope is host
// or all, for ignore and unignore.
type bulkRequest struct {
	Action string      `json:"action"`
	IDs    []uuid.UUID `json:"ids"`
	Until  *time.Time  `json:"until,omitempty"`
	Scope  string      `json:"scope,omitempty"`
}

// bulkResult is the result of one id: the new alert, or the error.
type bulkResult struct {
	ID    uuid.UUID   `json:"id"`
	Alert *wire.Alert `json:"alert,omitempty"`
	Error string      `json:"error,omitempty"`
}

// bulkActions maps the action names of a bulk request.
var bulkActions = map[string]actions.AlertAction{
	"ack":      {Kind: actions.ActAck},
	"unack":    {Kind: actions.ActAck, Undo: true},
	"snooze":   {Kind: actions.ActSnooze},
	"unsnooze": {Kind: actions.ActSnooze, Undo: true},
	"ignore":   {Kind: actions.ActIgnore},
	"unignore": {Kind: actions.ActIgnore, Undo: true},
}

// handleBulkAlerts applies one action to up to 200 alerts. It answers 200
// with a result for each id, also when some ids fail. It writes one log
// batch, and one list edit for each node (or one for the org).
func (s *Server) handleBulkAlerts(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	var req bulkRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	act, ok := bulkActions[req.Action]
	switch {
	case !ok:
		return echo.NewHTTPError(http.StatusBadRequest, "bad action")
	case len(req.IDs) == 0:
		return echo.NewHTTPError(http.StatusBadRequest, "ids is empty")
	case len(req.IDs) > MaxBulkAlerts:
		return echo.NewHTTPError(http.StatusBadRequest, "at most 200 ids")
	}
	act.Until = req.Until
	if act.Kind == actions.ActIgnore {
		if r, ok := authctx.RoleFrom(c); !ok || !r.Allows(store.RoleAdmin) {
			return echo.NewHTTPError(http.StatusForbidden, "role admin needed")
		}
	}
	res, err := s.actions.MarkAlerts(c.Request().Context(), actions.NewActor(t, "", ""), req.IDs, act, actions.IgnoreScope(req.Scope))
	if err != nil {
		return actionError(err)
	}
	results := make([]bulkResult, len(res))
	for i, r := range res {
		results[i] = bulkResult{ID: r.ID, Alert: r.Alert}
		if r.Err != nil {
			results[i].Error = r.Err.Error()
			continue
		}
		s.audit(c, req.Action, "alert", r.ID)
	}
	return c.JSON(http.StatusOK, map[string][]bulkResult{"results": results})
}
