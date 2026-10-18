package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/wire"
)

// Limits gives the plan limits of an org.
type Limits interface {
	// HistoryDays is the number of days of history that the org can
	// read. 0 means no limit.
	HistoryDays(ctx context.Context, orgID uuid.UUID) int
}

// historyRange is the node and the range of a history request.
type historyRange struct {
	clickhouse.RangeQuery
	// Clamped is true when the plan limit moved From later.
	Clamped bool
}

// parseRange reads :id, from and to. The default range is the span up
// to now. The plan limit clamps from.
func (s *Server) parseRange(c echo.Context, span time.Duration) (historyRange, error) {
	t, _ := authctx.TokenFrom(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return historyRange{}, echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	now := time.Now().UTC()
	to := now
	if v := c.QueryParam("to"); v != "" {
		to, _ = parseRangeBound(v, to)
	}
	from := to.Add(-span)
	if v := c.QueryParam("from"); v != "" {
		from, _ = parseRangeBound(v, from)
	}
	r := historyRange{RangeQuery: clickhouse.RangeQuery{OrgID: t.OrgID, NodeID: id, From: from, To: to}}
	r.From, r.Clamped = s.clampFrom(c.Request().Context(), t.OrgID, from, now)
	if r.To.Before(r.From) {
		r.To = r.From
	}
	return r, nil
}

// clampFrom moves from to the oldest time that the plan of the org
// allows.
func (s *Server) clampFrom(ctx context.Context, orgID uuid.UUID, from, now time.Time) (time.Time, bool) {
	if s.limits == nil {
		return from, false
	}
	days := s.limits.HistoryDays(ctx, orgID)
	if days <= 0 {
		return from, false
	}
	oldest := now.Add(-time.Duration(days) * 24 * time.Hour)
	if from.Before(oldest) {
		return oldest, true
	}
	return from, false
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

// handleHistory returns a time-bucketed aggregate for a single metric.
func (s *Server) handleHistory(c echo.Context) error {
	r, err := s.parseRange(c, time.Hour)
	if err != nil {
		return err
	}
	metric := c.QueryParam("metric")
	if metric == "" {
		metric = "cpu"
	}
	step := time.Minute
	if v := c.QueryParam("step"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			step = d
		}
	}
	if err := s.requireCH(c); err != nil {
		return err
	}

	pts, res, err := s.ch.QueryHistory(c.Request().Context(), clickhouse.HistoryQuery{
		OrgID:  r.OrgID,
		NodeID: r.NodeID,
		Metric: metric,
		From:   r.From,
		To:     r.To,
		Step:   step,
	})
	if errors.Is(err, clickhouse.ErrUnknownMetric) {
		return echo.NewHTTPError(http.StatusBadRequest, "unknown metric")
	}
	if err != nil {
		return err
	}
	out := wire.NodeHistoryResponse{
		NodeID:     r.NodeID,
		Metric:     metric,
		Resolution: resolution(res),
		Clamped:    r.Clamped,
		Points:     []wire.NodeHistoryPoint{},
	}
	for _, p := range pts {
		out.Points = append(out.Points, wire.NodeHistoryPoint{TS: p.TS, Value: p.Value, Max: p.Max})
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
