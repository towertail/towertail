package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/wire"
)

const (
	maxProcessPoints = 300
	maxProcessLimit  = 50
	defaultSnapLimit = 20
	maxProcessSeries = 120
)

// handleProcesses returns the top processes of a node in a range.
func (s *Server) handleProcesses(c echo.Context) error {
	id, err := parseNodeID(c)
	if err != nil {
		return err
	}
	r := s.parseRange(c, time.Hour)
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
	var series int
	if v := c.QueryParam("series"); v != "" {
		series, err = strconv.Atoi(v)
		if err != nil || series < 0 || series > maxProcessSeries {
			return echo.NewHTTPError(http.StatusBadRequest, "bad series")
		}
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
	if series > 0 && len(procs) > 0 {
		if err := s.addProcessSeries(c, &out, r.RangeQuery, series); err != nil {
			return err
		}
	}
	return c.JSON(http.StatusOK, out)
}

// addProcessSeries gives each row of out a series of about n points.
func (s *Server) addProcessSeries(c echo.Context, out *wire.NodeProcessesResponse, r clickhouse.RangeQuery, n int) error {
	span := r.To.Sub(r.From)
	step := clickhouse.SeriesStep(span, time.Since(r.From), n)
	keys := make([]wire.ProcessKey, len(out.Processes))
	for i, p := range out.Processes {
		keys[i] = p.Key
	}
	pts, err := s.ch.ProcessSeries(c.Request().Context(), clickhouse.ProcSeriesQuery{RangeQuery: r, Keys: keys, Step: step})
	if err != nil {
		return err
	}
	from := alignDown(r.From, step)
	count := int(r.To.Sub(from)/step) + 1
	out.SeriesFrom, out.SeriesStepS = &from, int(step/time.Second)
	for i := range out.Processes {
		out.Processes[i].Series = denseSeries(pts[out.Processes[i].Key], from, step, count)
	}
	return nil
}

// alignDown gives the last multiple of step, since the Unix epoch, that
// is not after t.
func alignDown(t time.Time, step time.Duration) time.Time {
	s := int64(step / time.Second)
	return time.Unix(t.Unix()/s*s, 0).UTC()
}

// denseSeries puts the points on n slots from "from", one for each step.
// A slot with no point is 0.
func denseSeries(pts []clickhouse.ProcPoint, from time.Time, step time.Duration, n int) *wire.ProcSeries {
	out := &wire.ProcSeries{CPU: make([]float64, n), RSS: make([]uint64, n), IO: make([]float64, n)}
	for _, p := range pts {
		i := int(p.TS.Sub(from) / step)
		if i < 0 || i >= n {
			continue
		}
		out.CPU[i], out.RSS[i], out.IO[i] = p.CPUPct, p.RSS, p.IOBps
	}
	return out
}

// handleProcessesAt returns the top processes of a node at one time.
func (s *Server) handleProcessesAt(c echo.Context) error {
	id, err := parseNodeID(c)
	if err != nil {
		return err
	}
	at := time.Now().UTC()
	if v := c.QueryParam("ts"); v != "" {
		at = parseRangeBound(v, time.Time{})
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
	t, _ := authctx.TokenFrom(c)
	out, err := s.query.ProcessesAt(c.Request().Context(), query.SnapshotRequest{OrgID: t.OrgID, NodeID: id, At: at, By: by, Limit: limit})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// handleProcess returns the points and the facts of one process.
func (s *Server) handleProcess(c echo.Context) error {
	id, err := parseNodeID(c)
	if err != nil {
		return err
	}
	key, err := wire.ParseProcessKey(c.Param("key"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad key")
	}
	r := s.parseRange(c, time.Hour)
	r.NodeID = id
	if err := s.requireCH(c); err != nil {
		return err
	}
	ctx := c.Request().Context()
	span, age := r.To.Sub(r.From), time.Since(r.From)
	step := clickhouse.FitStep(span, age, max(historyStep(c), (span+maxProcessPoints-1)/maxProcessPoints))
	cmd, repo, err := s.ch.ProcessInfo(ctx, r.OrgID, id, key, r.From, r.To)
	if errors.Is(err, clickhouse.ErrNoProcess) {
		return echo.NewHTTPError(http.StatusNotFound, "process not found")
	}
	if err != nil {
		return err
	}
	pts, err := s.ch.ProcessSeries(ctx, clickhouse.ProcSeriesQuery{RangeQuery: r.RangeQuery, Keys: []wire.ProcessKey{key}, Step: step})
	if err != nil {
		return err
	}
	out := wire.ProcessDetail{
		NodeID: id, Key: key, Name: key.Name, Cmd: cmd, Repo: repo,
		From: r.From, To: r.To, Resolution: resolution(step), Clamped: r.Clamped,
		Points: make([]wire.ProcPoint, 0, len(pts[key])),
	}
	for _, p := range pts[key] {
		out.Points = append(out.Points, wire.ProcPoint{TS: p.TS, CPUPct: p.CPUPct, RSS: p.RSS, IOBps: p.IOBps})
	}
	if time.Since(r.To) <= time.Hour {
		f, ok, err := s.ch.ProcessFacts(ctx, r.OrgID, id, key, r.To)
		if err != nil {
			return err
		}
		if ok {
			out.Facts = &wire.ProcessFacts{
				SeenAt: f.SeenAt, PID: f.PID, PPID: f.PPID, Parent: f.Parent, User: f.User, Cwd: f.Cwd,
				State: f.State, Threads: f.Threads, StartTS: f.StartTS, ListenTCP: f.ListenTCP, ListenUDP: f.ListenUDP,
			}
		}
	}
	return c.JSON(http.StatusOK, out)
}
