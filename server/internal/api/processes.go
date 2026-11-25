package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/wire"
)

const (
	maxProcessLimit  = 50
	defaultSnapLimit = 20
)

// handleProcesses returns the top processes of a node in a range.
func (s *Server) handleProcesses(c echo.Context) error {
	id, err := parseNodeID(c)
	if err != nil {
		return err
	}
	r, err := s.parseRange(c, time.Hour)
	if err != nil {
		return err
	}
	r.NodeID = id
	limit := 10
	if v := c.QueryParam("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return echo.NewHTTPError(http.StatusBadRequest, "bad limit")
		}
		limit = min(n, maxProcessLimit)
	}
	by, err := query.ParseProcOrder(c.QueryParam("by"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := s.requireCH(c); err != nil {
		return err
	}
	procs, err := s.ch.TopProcesses(c.Request().Context(), r.RangeQuery, by, limit)
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
		out.Processes = append(out.Processes, wire.TopProcess{
			Key:  wire.ProcessKey{Name: p.Name, CmdHash: p.CmdHash},
			Name: p.Name, Cmd: p.Cmd, Repo: p.Repo, Cwd: p.Cwd, Parent: p.Parent,
			CPUPctAvg: p.CPUPctAvg, CPUPctMax: p.CPUPctMax, RSSMax: p.RSSMax, IOBpsAvg: p.IOBpsAvg,
		})
	}
	return c.JSON(http.StatusOK, out)
}

// handleProcessesAt returns the top processes of a node at one time.
func (s *Server) handleProcessesAt(c echo.Context) error {
	id, err := parseNodeID(c)
	if err != nil {
		return err
	}
	at := time.Now().UTC()
	if v := c.QueryParam("ts"); v != "" {
		at, _ = parseRangeBound(v, time.Time{})
		if at.IsZero() {
			return echo.NewHTTPError(http.StatusBadRequest, "bad ts")
		}
	}
	by, err := query.ParseProcOrder(c.QueryParam("by"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	limit := defaultSnapLimit
	if v := c.QueryParam("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxProcessLimit {
			return echo.NewHTTPError(http.StatusBadRequest, "bad limit")
		}
		limit = n
	}
	if err := s.requireCH(c); err != nil {
		return err
	}
	out := wire.ProcessSnapshot{NodeID: id, TS: at, By: string(by), Processes: []wire.SnapshotProcess{}}
	r := s.clampRange(c, at, at, time.Now().UTC())
	if r.Clamped {
		out.Clamped = true
		return c.JSON(http.StatusOK, out)
	}
	snap, err := s.ch.ProcessesAt(c.Request().Context(), clickhouse.SnapshotQuery{OrgID: r.OrgID, NodeID: id, At: at, By: by, Limit: limit})
	if err != nil {
		return err
	}
	out.TS, out.Tier, out.BucketS, out.Total = snap.TS, snap.Tier, int(snap.Bucket/time.Second), snap.Total
	out.Prev, out.Next = snap.Prev, snap.Next
	if snap.TS.IsZero() {
		out.TS = at
	}
	for _, p := range snap.Procs {
		out.Processes = append(out.Processes, wire.SnapshotProcess{
			Key: wire.ProcessKey{Name: p.Name, CmdHash: p.CmdHash},
			PID: p.PID, PPID: p.PPID, Parent: p.Parent, User: p.User,
			Name: p.Name, Cmd: p.Cmd, Repo: p.Repo, Cwd: p.Cwd,
			CPUPct: p.CPUPct, CPUPctMax: p.CPUPctMax, RSS: p.RSS, IOBps: p.IOBps,
			Threads: p.Threads, State: p.State, StartTS: p.StartTS,
		})
	}
	return c.JSON(http.StatusOK, out)
}
