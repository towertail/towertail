package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/wire"
)

const maxProcessLimit = 50

// handleProcesses returns the top processes of a node in a range.
func (s *Server) handleProcesses(c echo.Context) error {
	r, err := s.parseRange(c, time.Hour)
	if err != nil {
		return err
	}
	limit := 10
	if v := c.QueryParam("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return echo.NewHTTPError(http.StatusBadRequest, "bad limit")
		}
		limit = min(n, maxProcessLimit)
	}
	if err := s.requireCH(c); err != nil {
		return err
	}
	procs, err := s.ch.TopProcesses(c.Request().Context(), r.RangeQuery, limit)
	if err != nil {
		return err
	}
	out := wire.NodeProcessesResponse{
		NodeID:    r.NodeID,
		From:      r.From,
		To:        r.To,
		Clamped:   r.Clamped,
		Processes: make([]wire.TopProcess, 0, len(procs)),
	}
	for _, p := range procs {
		out.Processes = append(out.Processes, wire.TopProcess(p))
	}
	return c.JSON(http.StatusOK, out)
}
