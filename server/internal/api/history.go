package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/wire"
)

// Limits gives the plan limits of an org.
type Limits interface {
	// HistoryDays is the number of days of history that the org can
	// read. 0 means no limit.
	HistoryDays(ctx context.Context, orgID uuid.UUID) int
	// MaxNodes is the number of nodes that the org can have. A negative
	// value means no limit.
	MaxNodes(ctx context.Context, orgID uuid.UUID) int
}

// historyRange is the node and the range of a history request.
type historyRange struct {
	clickhouse.RangeQuery
	// Clamped is true when the plan limit moved From later.
	Clamped bool
}

// parseNodeID reads the :id of a node route.
func parseNodeID(c echo.Context) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return uuid.Nil, echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	return id, nil
}

// rangeBounds reads from and to. The default range is the span up to
// now.
func rangeBounds(c echo.Context, span time.Duration) (from, to, now time.Time) {
	now = time.Now().UTC()
	to = now
	if v := c.QueryParam("to"); v != "" {
		to, _ = parseRangeBound(v, to)
	}
	from = to.Add(-span)
	if v := c.QueryParam("from"); v != "" {
		from, _ = parseRangeBound(v, from)
	}
	return from, to, now
}

// parseRange reads from and to. The default range is the span up to
// now. The plan limit clamps from. The caller sets NodeID.
func (s *Server) parseRange(c echo.Context, span time.Duration) (historyRange, error) {
	from, to, now := rangeBounds(c, span)
	return s.clampRange(c, from, to, now), nil
}

// clampRange applies the plan limit of the token org to a range.
func (s *Server) clampRange(c echo.Context, from, to, now time.Time) historyRange {
	t, _ := authctx.TokenFrom(c)
	r := historyRange{RangeQuery: clickhouse.RangeQuery{OrgID: t.OrgID, From: from, To: to}}
	r.From, r.Clamped = s.query.Clamp(c.Request().Context(), t.OrgID, from, now)
	if r.To.Before(r.From) {
		r.To = r.From
	}
	return r
}

// requireCH answers 503 when ClickHouse is off or not ready.
func (s *Server) requireCH(c echo.Context) error {
	if s.ch == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse disabled — no history available")
	}
	if err := s.runtime.Require(c.Request().Context(), "clickhouse"); err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse not ready")
	}
	return nil
}

// Limits of the multi-metric and fleet history requests.
const (
	maxHistoryMetrics  = 8
	maxFleetNodes      = 200
	maxFleetRange      = 24 * time.Hour
	defaultFleetRange  = 30 * time.Minute
	defaultHistoryStep = time.Minute
)

// parseIDList reads a comma list of UUIDs. It drops duplicates.
func parseIDList(s string, max int) ([]uuid.UUID, error) {
	parts := strings.Split(s, ",")
	out := make([]uuid.UUID, 0, len(parts))
	seen := make(map[uuid.UUID]bool, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := uuid.Parse(p)
		if err != nil {
			return nil, errors.New("bad id")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) > max {
			return nil, errors.New("too many")
		}
	}
	if len(out) == 0 {
		return nil, errors.New("empty list")
	}
	return out, nil
}

// parseMetricList reads a comma list of metric names. It drops
// duplicates.
func parseMetricList(s string, max int) ([]string, error) {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) > max {
			return nil, errors.New("too many")
		}
	}
	if len(out) == 0 {
		return nil, errors.New("empty list")
	}
	return out, nil
}

// historyStep reads the step. The default is 1 minute.
func historyStep(c echo.Context) time.Duration {
	if v := c.QueryParam("step"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultHistoryStep
}

// historyQueryError maps the errors of a history query to a status.
func historyQueryError(err error) error {
	switch {
	case errors.Is(err, clickhouse.ErrUnknownMetric):
		return echo.NewHTTPError(http.StatusBadRequest, "unknown metric")
	case errors.Is(err, clickhouse.ErrNoRollup):
		return echo.NewHTTPError(http.StatusBadRequest, "metric has no rollup for this range")
	}
	return err
}

// seriesOf converts the points of the metrics of one node.
func seriesOf(metrics []string, in map[string][]clickhouse.HistoryPoint) map[string][]wire.NodeHistoryPoint {
	out := make(map[string][]wire.NodeHistoryPoint, len(metrics))
	for _, m := range metrics {
		pts := make([]wire.NodeHistoryPoint, 0, len(in[m]))
		for _, p := range in[m] {
			pts = append(pts, wire.NodeHistoryPoint{TS: p.TS, Value: p.Value, Max: p.Max})
		}
		out[m] = pts
	}
	return out
}

// handleHistory returns a time-bucketed aggregate for a single metric,
// or for a list of metrics with "metrics=a,b".
func (s *Server) handleHistory(c echo.Context) error {
	id, err := parseNodeID(c)
	if err != nil {
		return err
	}
	step := historyStep(c)
	if v := c.QueryParam("metrics"); v != "" {
		r, err := s.parseRange(c, time.Hour)
		if err != nil {
			return err
		}
		r.NodeID = id
		return s.handleHistoryMulti(c, r, v, step)
	}
	metric := c.QueryParam("metric")
	if metric == "" {
		metric = "cpu"
	}
	if err := s.requireCH(c); err != nil {
		return err
	}
	t, _ := authctx.TokenFrom(c)
	from, to, _ := rangeBounds(c, time.Hour)
	pts, res, clamped, err := s.query.History(c.Request().Context(), query.HistoryRequest{
		OrgID: t.OrgID, NodeID: id, Metric: metric, From: from, To: to, Step: step,
	})
	if err != nil {
		return historyQueryError(err)
	}
	out := wire.NodeHistoryResponse{
		NodeID:     id,
		Metric:     metric,
		Resolution: resolution(res),
		Clamped:    clamped,
		Points:     []wire.NodeHistoryPoint{},
	}
	for _, p := range pts {
		out.Points = append(out.Points, wire.NodeHistoryPoint{TS: p.TS, Value: p.Value, Max: p.Max})
	}
	return c.JSON(http.StatusOK, out)
}

// handleHistoryMulti answers the "metrics" form of the node history.
func (s *Server) handleHistoryMulti(c echo.Context, r historyRange, list string, step time.Duration) error {
	metrics, err := parseMetricList(list, maxHistoryMetrics)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad metrics: "+err.Error())
	}
	if err := s.requireCH(c); err != nil {
		return err
	}
	got, res, err := s.ch.QueryHistoryMulti(c.Request().Context(), clickhouse.HistoryMultiQuery{
		OrgID:   r.OrgID,
		NodeIDs: []uuid.UUID{r.NodeID},
		Metrics: metrics,
		From:    r.From,
		To:      r.To,
		Step:    step,
	})
	if err != nil {
		return historyQueryError(err)
	}
	return c.JSON(http.StatusOK, wire.NodeHistoryMultiResponse{
		NodeHistorySeries: wire.NodeHistorySeries{NodeID: r.NodeID, Series: seriesOf(metrics, got[r.NodeID])},
		From:              r.From,
		To:                r.To,
		Resolution:        resolution(res),
		Clamped:           r.Clamped,
	})
}

// parseFleetRange reads the range of a fleet request. A range longer
// than 24 h is an error. The check uses the requested range, before the
// plan limit.
func (s *Server) parseFleetRange(c echo.Context) (historyRange, error) {
	from, to, now := rangeBounds(c, defaultFleetRange)
	if to.Sub(from) > maxFleetRange {
		return historyRange{}, echo.NewHTTPError(http.StatusBadRequest, "range is longer than 24h")
	}
	return s.clampRange(c, from, to, now), nil
}

// handleFleetHistory returns the history of many metrics of many nodes.
// A node with no points, or of another org, has no entry.
func (s *Server) handleFleetHistory(c echo.Context) error {
	ids, err := parseIDList(c.QueryParam("nodes"), maxFleetNodes)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad nodes: "+err.Error())
	}
	metrics, err := parseMetricList(c.QueryParam("metrics"), maxHistoryMetrics)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad metrics: "+err.Error())
	}
	r, err := s.parseFleetRange(c)
	if err != nil {
		return err
	}
	step := historyStep(c)
	if err := s.requireCH(c); err != nil {
		return err
	}
	got, res, err := s.ch.QueryHistoryMulti(c.Request().Context(), clickhouse.HistoryMultiQuery{
		OrgID:   r.OrgID,
		NodeIDs: ids,
		Metrics: metrics,
		From:    r.From,
		To:      r.To,
		Step:    step,
	})
	if err != nil {
		return historyQueryError(err)
	}
	out := wire.FleetHistoryResponse{
		From:       r.From,
		To:         r.To,
		Resolution: resolution(res),
		Clamped:    r.Clamped,
		Nodes:      []wire.NodeHistorySeries{},
	}
	for _, id := range ids {
		if series, ok := got[id]; ok {
			out.Nodes = append(out.Nodes, wire.NodeHistorySeries{NodeID: id, Series: seriesOf(metrics, series)})
		}
	}
	return c.JSON(http.StatusOK, out)
}

// resolution is "raw" for one point for each sample, else the bucket.
func resolution(d time.Duration) string {
	if d == 0 {
		return "raw"
	}
	return d.String()
}

// parseRangeBound accepts RFC3339 absolute or "now-1h"-style relative
// forms. The default bound is supplied for relative parse failures.
func parseRangeBound(s string, fallback time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	// "now" / "now-1h"
	if s == "now" {
		return time.Now().UTC(), nil
	}
	if len(s) > 4 && s[:4] == "now-" {
		if d, err := time.ParseDuration(s[4:]); err == nil {
			return time.Now().UTC().Add(-d), nil
		}
	}
	return fallback, nil
}
