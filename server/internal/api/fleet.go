package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
)

const (
	defaultFleetHosts = 100
	maxFleetHosts     = 500
	defaultFleetProcs = 20
	defaultCompareWin = time.Hour
)

// intParam reads an integer query value of 1 or more. An empty value
// gives def. A value above max gives max.
func intParam(c echo.Context, name string, def, max int) (int, error) {
	v := c.QueryParam(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "bad "+name)
	}
	return min(n, max), nil
}

// nodeParam reads the optional node query value. No value gives uuid.Nil.
func nodeParam(c echo.Context) (uuid.UUID, error) {
	v := c.QueryParam("node")
	if v == "" {
		return uuid.Nil, nil
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return uuid.Nil, echo.NewHTTPError(http.StatusBadRequest, "bad node")
	}
	return id, nil
}

// fleetQueryError maps the errors of the query service to a status.
func fleetQueryError(err error) error {
	switch {
	case errors.Is(err, query.ErrNoHistory):
		return echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse disabled — no history available")
	case errors.Is(err, query.ErrBadSort), errors.Is(err, query.ErrBadOrder), errors.Is(err, query.ErrBadWindow):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, clickhouse.ErrUnknownMetric):
		return echo.NewHTTPError(http.StatusBadRequest, "unknown metric")
	}
	return err
}

// handleFleetHosts gives the hosts of the org with the latest values.
func (s *Server) handleFleetHosts(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	sort, err := query.ParseHostSort(c.QueryParam("sort"))
	if err != nil {
		return fleetQueryError(err)
	}
	limit, err := intParam(c, "limit", defaultFleetHosts, maxFleetHosts)
	if err != nil {
		return err
	}
	if err := s.requireCH(c); err != nil {
		return err
	}
	out, err := s.query.Hosts(c.Request().Context(), t.OrgID, sort, limit)
	if err != nil {
		return fleetQueryError(err)
	}
	return c.JSON(http.StatusOK, out)
}

// handleFleetProcesses gives the top processes of the fleet or of one
// host. The default range is the last hour.
func (s *Server) handleFleetProcesses(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	by, err := query.ParseProcOrder(c.QueryParam("by"))
	if err != nil {
		return fleetQueryError(err)
	}
	node, err := nodeParam(c)
	if err != nil {
		return err
	}
	limit, err := intParam(c, "limit", defaultFleetProcs, query.MaxProcs)
	if err != nil {
		return err
	}
	if err := s.requireCH(c); err != nil {
		return err
	}
	from, to, _ := rangeBounds(c, time.Hour)
	out, err := s.query.TopProcesses(c.Request().Context(), query.ProcQuery{
		OrgID: t.OrgID, NodeID: node, From: from, To: to, By: by, Limit: limit,
	})
	if err != nil {
		return fleetQueryError(err)
	}
	return c.JSON(http.StatusOK, out)
}

// handleFleetCompare compares a metric of the last window to the window
// before it.
func (s *Server) handleFleetCompare(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	metric := c.QueryParam("metric")
	if metric == "" {
		metric = "cpu"
	}
	window := defaultCompareWin
	if v := c.QueryParam("window"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "bad window")
		}
		window = d
	}
	node, err := nodeParam(c)
	if err != nil {
		return err
	}
	q := query.CompareQuery{OrgID: t.OrgID, NodeID: node, Metric: metric, Window: window}
	if err := q.Validate(); err != nil {
		return fleetQueryError(err)
	}
	if err := s.requireCH(c); err != nil {
		return err
	}
	out, err := s.query.Compare(c.Request().Context(), q)
	if err != nil {
		return fleetQueryError(err)
	}
	return c.JSON(http.StatusOK, out)
}
