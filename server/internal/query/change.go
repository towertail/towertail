package query

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/changepoint"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/wire"
)

const (
	// changeBuckets is the most buckets that ExplainChange reads.
	changeBuckets = 300
	// changeMinSegment and changePoints are the settings of the detector.
	changeMinSegment = 5
	changePoints     = 5
	// changeWindow is the longest window around a change point.
	changeWindow = time.Hour
	// maxChangeProcs and maxChangeAlerts cap the lists of the report.
	maxChangeProcs  = 10
	maxChangeAlerts = 20
)

// ChangeRequest asks why a metric of a node changed in the range. A zero
// To means now.
type ChangeRequest struct {
	OrgID, NodeID uuid.UUID
	Metric        string
	From, To      time.Time
}

// ChangePoint is a shift of the level of the metric. TS is the start of
// the first bucket of the new level.
type ChangePoint struct {
	TS                   time.Time
	Before, After, Delta float64
}

// ProcDelta is the change of one process across the largest change point,
// in the unit of the order: a percent of one core for cpu, bytes for mem
// and bytes each second for io. New means no row before the change point,
// and Gone means no row after it. A process outside the top 50 of a window
// counts as absent from it.
type ProcDelta struct {
	Key                  wire.ProcessKey
	Name, Cmd            string
	Before, After, Delta float64
	New, Gone            bool
}

// ChangeReport is the answer of ExplainChange. Points come with the
// largest shift first. Before and After are the windows of the process
// diff, and Processes and Alerts hold the diff and the alerts that started
// in both windows. They are empty when the series has no change point.
type ChangeReport struct {
	From, To      time.Time
	Bucket        time.Duration
	Clamped       bool
	Metric        string
	Points        []ChangePoint
	Before, After wire.Window
	By            clickhouse.ProcOrder
	Processes     []ProcDelta
	Alerts        []wire.Alert
}

// ExplainChange finds the level shifts of a metric and, for the largest,
// the processes and the alerts around it. The plan clamps From. It gives
// store.ErrNotFound for a node that the org does not have.
func (s *Service) ExplainChange(ctx context.Context, r ChangeRequest) (ChangeReport, error) {
	if !slices.Contains(clickhouse.MetricNames(), r.Metric) {
		return ChangeReport{}, clickhouse.ErrUnknownMetric
	}
	if _, err := s.node(ctx, r.OrgID, r.NodeID); err != nil {
		return ChangeReport{}, err
	}
	if s.ch == nil {
		return ChangeReport{}, ErrNoHistory
	}
	from, to, clamped := s.clampRange(ctx, r.OrgID, r.From, r.To, s.now().UTC())
	out := ChangeReport{
		From: from, To: to, Clamped: clamped, Metric: r.Metric,
		By: processOrder(r.Metric), Points: []ChangePoint{}, Processes: []ProcDelta{}, Alerts: []wire.Alert{},
	}
	if !from.Before(to) {
		return out, nil
	}
	step := ((to.Sub(from)/changeBuckets + time.Second - 1) / time.Second) * time.Second
	pts, bucket, err := s.ch.QueryHistory(ctx, clickhouse.HistoryQuery{OrgID: r.OrgID, NodeID: r.NodeID, Metric: r.Metric, From: from, To: to, Step: step})
	if err != nil {
		return ChangeReport{}, err
	}
	out.Bucket = bucket
	values := make([]float64, len(pts))
	for i, p := range pts {
		values[i] = p.Value
	}
	found := changepoint.NewDetector(changeMinSegment, changePoints).Find(values)
	if len(found) == 0 {
		return out, nil
	}
	for _, p := range found {
		out.Points = append(out.Points, ChangePoint{TS: pts[p.Index].TS, Before: p.Before, After: p.After, Delta: p.After - p.Before})
	}
	cp := out.Points[0].TS
	w := changeSide(out.Points, from, to, cp)
	out.Before = wire.Window{From: cp.Add(-w), To: cp}
	out.After = wire.Window{From: cp, To: cp.Add(w)}
	if out.Processes, err = s.processDeltas(ctx, r, out); err != nil {
		return ChangeReport{}, err
	}
	page, err := s.ListAlerts(ctx, AlertQuery{
		OrgID: r.OrgID, State: "all", Hosts: []uuid.UUID{r.NodeID}, From: out.Before.From, To: out.After.To,
		StartedFrom: &out.Before.From, StartedTo: &out.After.To, Sort: "start", Limit: maxChangeAlerts,
	})
	if err != nil {
		return ChangeReport{}, err
	}
	out.Alerts = page.Alerts
	return out, nil
}

// changeSide gives the length of the window on each side of the change
// point cp: the shorter distance to the next change point, or to the end
// of the range, but at most changeWindow.
func changeSide(points []ChangePoint, from, to, cp time.Time) time.Duration {
	prev, next := from, to
	for _, p := range points {
		switch {
		case p.TS.Before(cp) && p.TS.After(prev):
			prev = p.TS
		case p.TS.After(cp) && p.TS.Before(next):
			next = p.TS
		}
	}
	return min(changeWindow, cp.Sub(prev), next.Sub(cp))
}

// processOrder gives the order of the process diff that fits a metric.
func processOrder(metric string) clickhouse.ProcOrder {
	switch {
	case metric == "mem" || metric == "mem_pressure" || strings.HasPrefix(metric, "psi_mem"):
		return clickhouse.ProcByMem
	case metric == "disk_read" || metric == "disk_write" || strings.HasPrefix(metric, "psi_io"):
		return clickhouse.ProcByIO
	}
	return clickhouse.ProcByCPU
}

// processDeltas compares the top processes of the two windows. The change
// of the largest point sets the sort: the biggest rise first for a rise,
// the biggest fall first for a fall.
func (s *Service) processDeltas(ctx context.Context, r ChangeRequest, rep ChangeReport) ([]ProcDelta, error) {
	type pkey = wire.ProcessKey
	read := func(w wire.Window) (map[pkey]clickhouse.TopProc, error) {
		from, to, _ := s.clampRange(ctx, r.OrgID, w.From, w.To, s.now().UTC())
		if !from.Before(to) {
			return nil, nil
		}
		procs, err := s.ch.TopProcesses(ctx, clickhouse.RangeQuery{OrgID: r.OrgID, NodeID: r.NodeID, From: from, To: to}, rep.By, MaxProcs)
		if err != nil {
			return nil, err
		}
		m := make(map[pkey]clickhouse.TopProc, len(procs))
		for _, p := range procs {
			m[pkey{Name: p.Name, CmdHash: p.CmdHash}] = p
		}
		return m, nil
	}
	before, err := read(rep.Before)
	if err != nil {
		return nil, err
	}
	after, err := read(rep.After)
	if err != nil {
		return nil, err
	}
	value := func(p clickhouse.TopProc) float64 {
		switch rep.By {
		case clickhouse.ProcByMem:
			return float64(p.RSSMax)
		case clickhouse.ProcByIO:
			return p.IOBpsAvg
		}
		return p.CPUPctAvg
	}
	out := make([]ProcDelta, 0, len(before)+len(after))
	for k, a := range after {
		b, had := before[k]
		out = append(out, ProcDelta{Key: k, Name: a.Name, Cmd: a.Cmd, Before: value(b), After: value(a), Delta: value(a) - value(b), New: !had})
	}
	for k, b := range before {
		if _, kept := after[k]; !kept {
			out = append(out, ProcDelta{Key: k, Name: b.Name, Cmd: b.Cmd, Before: value(b), Delta: -value(b), Gone: true})
		}
	}
	dir := math.Copysign(1, rep.Points[0].Delta)
	slices.SortFunc(out, func(a, b ProcDelta) int {
		return cmp.Or(cmp.Compare(dir*b.Delta, dir*a.Delta), cmp.Compare(a.Name, b.Name), cmp.Compare(a.Key.CmdHash, b.Key.CmdHash))
	})
	return out[:min(len(out), maxChangeProcs)], nil
}
