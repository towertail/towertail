package api

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/server/internal/clickhouse"
	"github.com/towertail/server/internal/wire"
)

// handleHistory returns a time-bucketed aggregate for a single metric.
func (s *Server) handleHistory(c echo.Context) error {
	t, _ := tokenFromCtx(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	metric := c.QueryParam("metric")
	if metric == "" {
		metric = "cpu"
	}
	fromStr := c.QueryParam("from")
	toStr := c.QueryParam("to")
	stepStr := c.QueryParam("step")

	to := time.Now().UTC()
	from := to.Add(-1 * time.Hour)
	if toStr != "" {
		if parsed, err := parseRangeBound(toStr, to); err == nil {
			to = parsed
		}
	}
	if fromStr != "" {
		if parsed, err := parseRangeBound(fromStr, from); err == nil {
			from = parsed
		}
	}
	step := time.Minute
	if stepStr != "" {
		if d, err := time.ParseDuration(stepStr); err == nil {
			step = d
		}
	}

	if s.ch == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse disabled — no history available")
	}
	if err := s.runtime.Require(c.Request().Context(), "clickhouse"); err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse not ready")
	}

	pts, err := s.ch.QueryHistory(c.Request().Context(), clickhouse.HistoryQuery{
		OrgID:      t.OrgID,
		NodeID:     id,
		Metric:     metric,
		From:       from,
		To:         to,
		Resolution: step,
	})
	if err != nil {
		return err
	}
	out := wire.NodeHistoryResponse{
		NodeID:     id,
		Metric:     metric,
		Resolution: step.String(),
	}
	for _, p := range pts {
		out.Points = append(out.Points, wire.NodeHistoryPoint{TS: p.TS, Value: p.Value, Max: p.Max})
	}
	return c.JSON(http.StatusOK, out)
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
