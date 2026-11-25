package api

import (
	"context"
	"errors"
	"fmt"
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
	AlertsByID(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]clickhouse.AlertRow, error)
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
	var err error
	if q.From, err = parseRangeBound(p("from"), now.Add(-7*24*time.Hour)); err != nil {
		return q, err
	}
	if q.To, err = parseRangeBound(p("to"), now); err != nil {
		return q, err
	}
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
	return s.markAlert(c, alertAction{Kind: actAck})
}

func (s *Server) handleUnackAlert(c echo.Context) error {
	return s.markAlert(c, alertAction{Kind: actAck, Undo: true})
}

func (s *Server) handleSnoozeAlert(c echo.Context) error {
	return s.markAlert(c, alertAction{Kind: actSnooze})
}

func (s *Server) handleUnsnoozeAlert(c echo.Context) error {
	return s.markAlert(c, alertAction{Kind: actSnooze, Undo: true})
}

// Kinds of alertAction.
const (
	actAck    = "ack"
	actSnooze = "snooze"
	actIgnore = "ignore"
)

// alertAction is one change of an alert. Undo takes it back.
type alertAction struct {
	Kind  string
	Until *time.Time
	Scope string
	Undo  bool
}

// applyAlertAction sets or takes back the ack or the snooze of an alert,
// tells the alerter, and gives the log line and the new alert. For ignore
// it only checks that the alert has a process: the caller writes the
// lists, and the line has no text yet. An undo of an ignore has no line
// (Kind is empty). The caller writes the line, and sends the frame.
func (s *Server) applyAlertAction(ctx context.Context, org, by uuid.UUID, row clickhouse.AlertRow, mark clickhouse.AlertMark, act alertAction, now time.Time) (notify.Line, wire.Alert, error) {
	a := query.AlertFromRow(row, mark)
	line := notify.Line{OrgID: org, NodeID: a.NodeID, AlertID: a.ID, At: now, User: &by}
	ack := notify.Ack{OrgID: org, EventID: a.ID, AlertID: a.ID, NodeID: a.NodeID, Metric: a.Metric, By: by, At: now, Undo: act.Undo}
	switch act.Kind {
	case actAck:
		line.Kind = notify.LineAcked
		a.AckedBy, a.AckedAt = &by, &now
		if act.Undo {
			line.Kind = notify.LineUnacked
			a.AckedBy, a.AckedAt = nil, nil
		}
	case actSnooze:
		ack.Snooze = true
		line.Kind, line.Until = notify.LineSnoozed, act.Until
		a.SnoozedUntil, ack.Until = act.Until, act.Until
		if act.Undo {
			line.Kind, line.Until = notify.LineUnsnoozed, nil
			a.SnoozedUntil = nil
		}
	case actIgnore:
		if a.Kind != "long_process" || a.Proc == nil || a.Proc.Name == "" {
			return notify.Line{}, a, echo.NewHTTPError(http.StatusBadRequest, "only a long_process alert has a process to ignore")
		}
		if act.Undo {
			return notify.Line{}, a, nil
		}
		line.Kind = notify.LineClosed
		return line, a, nil
	}
	s.tellAcked(ctx, a, ack)
	return line, a, nil
}

// markAlert sets or takes back the ack or the snooze of an open alert.
func (s *Server) markAlert(c echo.Context, act alertAction) error {
	t, _ := authctx.TokenFrom(c)
	now := time.Now().UTC()
	if act.Kind == actSnooze && !act.Undo {
		u, err := parseFuture(c.QueryParam("until"), now)
		if err != nil {
			return err
		}
		act.Until = &u
	}
	row, mark, err := s.findAlert(c, true)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	line, a, err := s.applyAlertAction(ctx, t.OrgID, ackedBy(t), row, mark, act, now)
	if err != nil {
		return err
	}
	s.writeJournal(ctx, line)
	s.audit(c, string(line.Kind), "alert", a.ID)
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

func (s *Server) handleIgnoreAlert(c echo.Context) error   { return s.ignoreAlert(c, false) }
func (s *Server) handleUnignoreAlert(c echo.Context) error { return s.ignoreAlert(c, true) }

// ignoreAlert adds the process name of a long_process alert to the ignore
// list of the host or of the org, or removes it. The engine closes the
// open alert with no notification.
func (s *Server) ignoreAlert(c echo.Context, undo bool) error {
	t, _ := authctx.TokenFrom(c)
	scope := c.QueryParam("scope")
	if scope != "host" && scope != "all" {
		return echo.NewHTTPError(http.StatusBadRequest, "scope must be host or all")
	}
	row, mark, err := s.findAlert(c, false)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	now := time.Now().UTC()
	line, a, err := s.applyAlertAction(ctx, t.OrgID, ackedBy(t), row, mark, alertAction{Kind: actIgnore, Scope: scope, Undo: undo}, now)
	if err != nil {
		return err
	}
	text, errs := s.writeIgnores(ctx, t.OrgID, scope, undo, []ignoreItem{{node: a.NodeID, name: a.Proc.Name}})
	if errs[0] != nil {
		return errs[0]
	}
	if !undo {
		line.Text = text[0]
		s.writeJournal(ctx, line)
	}
	action := actIgnore
	if undo {
		action = "unignore"
	}
	s.audit(c, action, "alert", a.ID, "name", a.Proc.Name, "scope", scope)
	s.broadcastAlert(t.OrgID, a)
	return c.JSON(http.StatusOK, a)
}

// ignoreItem is a process name to add to the ignore list of a node.
type ignoreItem struct {
	node uuid.UUID
	name string
}

// writeIgnores adds the names to the ignore lists, or removes them, with
// one write for each node ("host") or one write for the org ("all"). It
// gives the log text and the error of each item. A failed write gives the
// error to each item of its node.
func (s *Server) writeIgnores(ctx context.Context, org uuid.UUID, scope string, undo bool, items []ignoreItem) (text []string, errs []error) {
	edit := func(l []string, names []string) []string {
		l = slices.DeleteFunc(slices.Clone(l), func(x string) bool {
			return slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(x, n) })
		})
		if undo {
			return l
		}
		for _, n := range names {
			if !slices.ContainsFunc(l, func(x string) bool { return strings.EqualFold(x, n) }) {
				l = append(l, n)
			}
		}
		return l
	}
	text, errs = make([]string, len(items)), make([]error, len(items))
	if scope == "all" {
		names := make([]string, len(items))
		for i, it := range items {
			names[i] = it.name
			text[i] = it.name + " ignored on all hosts"
		}
		cfg, err := s.store.EditSettings(ctx, org, func(cfg *store.ServerSettings) error {
			cfg.LongProc.Ignore = edit(cfg.LongProc.Ignore, names)
			return nil
		})
		if err != nil {
			return nil, slices.Repeat([]error{err}, len(items))
		}
		if s.alerter != nil {
			s.alerter.InvalidateSettings(org)
		}
		s.broadcastSettings(org, cfg)
		return text, errs
	}
	var nodes []uuid.UUID
	names := map[uuid.UUID][]string{}
	for _, it := range items {
		if _, ok := names[it.node]; !ok {
			nodes = append(nodes, it.node)
		}
		names[it.node] = append(names[it.node], it.name)
	}
	for _, id := range nodes {
		n, err := s.store.EditNode(ctx, org, id, func(n *store.Node) error {
			n.IgnoredProcs = edit(n.IgnoredProcs, names[id])
			return nil
		})
		for i, it := range items {
			switch {
			case it.node != id:
			case err != nil:
				errs[i] = err
			default:
				text[i] = it.name + " ignored on " + n.DisplayName
			}
		}
		if err == nil {
			s.broadcastNode(org, n)
		}
	}
	return text, errs
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
var bulkActions = map[string]alertAction{
	"ack":      {Kind: actAck},
	"unack":    {Kind: actAck, Undo: true},
	"snooze":   {Kind: actSnooze},
	"unsnooze": {Kind: actSnooze, Undo: true},
	"ignore":   {Kind: actIgnore},
	"unignore": {Kind: actIgnore, Undo: true},
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
	now := time.Now().UTC()
	switch act.Kind {
	case actSnooze:
		if !act.Undo {
			if req.Until == nil || !req.Until.After(now) {
				return echo.NewHTTPError(http.StatusBadRequest, "until must be in the future")
			}
			u := req.Until.UTC()
			act.Until = &u
		}
	case actIgnore:
		if r, ok := authctx.RoleFrom(c); !ok || !r.Allows(store.RoleAdmin) {
			return echo.NewHTTPError(http.StatusForbidden, "role admin needed")
		}
		if req.Scope != "host" && req.Scope != "all" {
			return echo.NewHTTPError(http.StatusBadRequest, "scope must be host or all")
		}
		act.Scope = req.Scope
	}
	if err := s.alertsReady(); err != nil {
		return err
	}
	ctx := c.Request().Context()
	order := dedupeIDs(req.IDs)
	var rows map[uuid.UUID]clickhouse.AlertRow
	marks := map[uuid.UUID]clickhouse.AlertMark{}
	if s.alerts != nil {
		var err error
		if rows, err = s.alerts.AlertsByID(ctx, t.OrgID, order); err != nil {
			return err
		}
		aids := make([]uuid.UUID, 0, len(rows))
		for _, r := range rows {
			aids = append(aids, alertIDOf(r))
		}
		if marks, err = s.alerts.AlertMarks(ctx, t.OrgID, aids); err != nil {
			return err
		}
	}

	by := ackedBy(t)
	results := make([]bulkResult, len(order))
	var (
		lines   []notify.Line
		items   []ignoreItem
		itemAt  []int
		ignored []notify.Line
	)
	for i, id := range order {
		results[i].ID = id
		row, found := rows[id]
		switch {
		case !found:
			results[i].Error = "alert not found"
			continue
		case act.Kind != actIgnore && row.Tint == "ok":
			results[i].Error = "alert is not open"
			continue
		}
		line, a, err := s.applyAlertAction(ctx, t.OrgID, by, row, marks[alertIDOf(row)], act, now)
		if err != nil {
			results[i].Error = errorText(err)
			continue
		}
		results[i].Alert = &a
		if act.Kind == actIgnore {
			items, itemAt = append(items, ignoreItem{node: a.NodeID, name: a.Proc.Name}), append(itemAt, i)
			ignored = append(ignored, line)
			continue
		}
		lines = append(lines, line)
	}
	if len(items) > 0 {
		text, errs := s.writeIgnores(ctx, t.OrgID, act.Scope, act.Undo, items)
		for n, i := range itemAt {
			if errs[n] != nil {
				results[i].Alert, results[i].Error = nil, errorText(errs[n])
				continue
			}
			if !act.Undo {
				ignored[n].Text = text[n]
				lines = append(lines, ignored[n])
			}
		}
	}
	s.writeJournal(ctx, lines...)
	for _, r := range results {
		if r.Alert == nil {
			continue
		}
		s.audit(c, req.Action, "alert", r.ID)
		s.broadcastAlert(t.OrgID, *r.Alert)
	}
	return c.JSON(http.StatusOK, map[string][]bulkResult{"results": results})
}

// dedupeIDs drops the repeated ids and keeps the order.
func dedupeIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// alertIDOf is the alert id of the row. An old row has the id of its event.
func alertIDOf(r clickhouse.AlertRow) uuid.UUID {
	if r.AlertID != uuid.Nil {
		return r.AlertID
	}
	return r.EventID
}

// errorText is the message of the error for the result of one id.
func errorText(err error) string {
	var he *echo.HTTPError
	if errors.As(err, &he) {
		return fmt.Sprint(he.Message)
	}
	return err.Error()
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

// findAlert finds the alert of the path id in the org of the caller. With
// open set, an alert that is closed gives 404.
func (s *Server) findAlert(c echo.Context, open bool) (clickhouse.AlertRow, clickhouse.AlertMark, error) {
	t, _ := authctx.TokenFrom(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return clickhouse.AlertRow{}, clickhouse.AlertMark{}, echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	if s.alerts == nil {
		return clickhouse.AlertRow{}, clickhouse.AlertMark{}, echo.NewHTTPError(http.StatusNotFound, "alert not found")
	}
	if !s.alerts.Ready() {
		return clickhouse.AlertRow{}, clickhouse.AlertMark{}, echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse not ready")
	}
	ctx := c.Request().Context()
	r, err := s.alerts.Alert(ctx, t.OrgID, id)
	if errors.Is(err, clickhouse.ErrNoAlert) || (err == nil && open && r.Tint == "ok") {
		return clickhouse.AlertRow{}, clickhouse.AlertMark{}, echo.NewHTTPError(http.StatusNotFound, "alert not found")
	}
	if err != nil {
		return clickhouse.AlertRow{}, clickhouse.AlertMark{}, err
	}
	aid := id
	if r.AlertID != uuid.Nil {
		aid = r.AlertID
	}
	marks, err := s.alerts.AlertMarks(ctx, t.OrgID, []uuid.UUID{aid})
	if err != nil {
		return clickhouse.AlertRow{}, clickhouse.AlertMark{}, err
	}
	return r, marks[aid], nil
}
