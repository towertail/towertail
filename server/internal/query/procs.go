package query

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/wire"
)

// procRetention is the longest history of the process rollups.
const procRetention = 365 * 24 * time.Hour

// resolveTop is the number of processes that a name search reads.
const resolveTop = 50

// SnapshotRequest asks for the top processes of a node at one time. A
// zero At means now. A Limit of 0 gives 20.
type SnapshotRequest struct {
	OrgID, NodeID uuid.UUID
	At            time.Time
	By            clickhouse.ProcOrder
	Limit         int
}

// ProcessesAt gives the top processes of a node at one time. An At older
// than the plan gives Clamped and no processes.
func (s *Service) ProcessesAt(ctx context.Context, r SnapshotRequest) (wire.ProcessSnapshot, error) {
	by, err := ParseProcOrder(string(r.By))
	if err != nil {
		return wire.ProcessSnapshot{}, err
	}
	now := s.now().UTC()
	at := r.At
	if at.IsZero() {
		at = now
	}
	out := wire.ProcessSnapshot{NodeID: r.NodeID, TS: at, By: string(by), Processes: []wire.SnapshotProcess{}}
	if _, clamped := s.Clamp(ctx, r.OrgID, at, now); clamped {
		out.Clamped = true
		return out, nil
	}
	if s.ch == nil {
		return wire.ProcessSnapshot{}, ErrNoHistory
	}
	limit := r.Limit
	if limit <= 0 {
		limit = defaultProc
	}
	snap, err := s.ch.ProcessesAt(ctx, clickhouse.SnapshotQuery{OrgID: r.OrgID, NodeID: r.NodeID, At: at, By: by, Limit: min(limit, MaxProcs)})
	if err != nil {
		return wire.ProcessSnapshot{}, err
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
	return out, nil
}

// ProcStart tells when a process started. StartedAt is the start time that
// the sampler read. When Exact is false, StartedAt is nil and FirstSeen is
// the oldest stored time of the process, so the process is older than that.
type ProcStart struct {
	PID       int32
	StartedAt *time.Time
	FirstSeen *time.Time
	Exact     bool
}

// ProcessStarts gives the start of each key at the time. Within 1 hour of
// now it reads the raw rows. A key with no raw start time gets the oldest
// rollup bucket since the start of the plan. A key with no row has no entry.
func (s *Service) ProcessStarts(ctx context.Context, orgID uuid.UUID, keys []clickhouse.ProcStartKey, at time.Time) (map[clickhouse.ProcStartKey]ProcStart, error) {
	out := make(map[clickhouse.ProcStartKey]ProcStart, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	if s.ch == nil {
		return nil, ErrNoHistory
	}
	now := s.now().UTC()
	if now.Sub(at) <= time.Hour {
		raw, err := s.ch.ProcessStarts(ctx, orgID, keys, at)
		if err != nil {
			return nil, err
		}
		for k, r := range raw {
			out[k] = ProcStart{PID: r.PID, StartedAt: r.StartTS, Exact: r.StartTS != nil}
		}
	}
	var rest []clickhouse.ProcStartKey
	for _, k := range keys {
		if !out[k].Exact {
			rest = append(rest, k)
		}
	}
	if len(rest) == 0 {
		return out, nil
	}
	since, _ := s.Clamp(ctx, orgID, now.Add(-procRetention), now)
	seen, err := s.ch.FirstSeen(ctx, orgID, rest, since)
	if err != nil {
		return nil, err
	}
	for k, t := range seen {
		if out[k].Exact {
			continue
		}
		ps := out[k]
		ps.FirstSeen = &t
		out[k] = ps
	}
	return out, nil
}

// ResolveProcess gives the key of a process of a node. ref is the text of
// a key, or a process name with no case. A name is searched in the top 50
// processes of the range. The plan clamps the range.
func (s *Service) ResolveProcess(ctx context.Context, orgID, nodeID uuid.UUID, ref string, from, to time.Time) (wire.ProcessKey, error) {
	ref = strings.TrimSpace(ref)
	if key, err := wire.ParseProcessKey(ref); err == nil {
		return key, nil
	}
	if s.ch == nil {
		return wire.ProcessKey{}, ErrNoHistory
	}
	from, to, _ = s.clampRange(ctx, orgID, from, to, s.now().UTC())
	procs, err := s.ch.TopProcesses(ctx, clickhouse.RangeQuery{OrgID: orgID, NodeID: nodeID, From: from, To: to}, clickhouse.ProcByCPU, resolveTop)
	if err != nil {
		return wire.ProcessKey{}, err
	}
	var (
		keys []wire.ProcessKey
		cmds []string
	)
	for _, p := range procs {
		key := wire.ProcessKey{Name: p.Name, CmdHash: p.CmdHash}
		if !strings.EqualFold(p.Name, ref) || slices.Contains(keys, key) {
			continue
		}
		keys = append(keys, key)
		cmds = append(cmds, fmt.Sprintf("%s (%s)", key, shorten(p.Cmd, 80)))
	}
	switch len(keys) {
	case 0:
		return wire.ProcessKey{}, fmt.Errorf("%w: %q", ErrProcessNotFound, ref)
	case 1:
		return keys[0], nil
	}
	return wire.ProcessKey{}, fmt.Errorf("%w: %q matches %s", ErrProcessAmbiguous, ref, strings.Join(cmds, ", "))
}

func shorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// ProcessHistoryRequest asks for the points of one process. A MaxPoints of
// 0 gives 300.
type ProcessHistoryRequest struct {
	OrgID, NodeID uuid.UUID
	Key           wire.ProcessKey
	From, To      time.Time
	MaxPoints     int
}

// ProcessHistory is one process over a range. Facts is nil when To is more
// than 1 hour ago or the process has no raw row.
type ProcessHistory struct {
	From, To time.Time
	Step     time.Duration
	Clamped  bool
	Cmd      string
	Repo     string
	Points   []clickhouse.ProcPoint
	Facts    *clickhouse.ProcFacts
	Start    ProcStart
}

// ProcessHistory gives the points, the facts and the start of a process.
// The plan clamps From. A process with no row in the range gives
// ErrProcessNotFound.
func (s *Service) ProcessHistory(ctx context.Context, r ProcessHistoryRequest) (ProcessHistory, error) {
	if s.ch == nil {
		return ProcessHistory{}, ErrNoHistory
	}
	maxPoints := r.MaxPoints
	if maxPoints <= 0 {
		maxPoints = 300
	}
	now := s.now().UTC()
	from, to, clamped := s.clampRange(ctx, r.OrgID, r.From, r.To, now)
	span := to.Sub(from)
	out := ProcessHistory{From: from, To: to, Clamped: clamped}
	out.Step = clickhouse.FitStep(span, now.Sub(from), (span+time.Duration(maxPoints)-1)/time.Duration(maxPoints))
	var err error
	out.Cmd, out.Repo, err = s.ch.ProcessInfo(ctx, r.OrgID, r.NodeID, r.Key, from, to)
	if errors.Is(err, clickhouse.ErrNoProcess) {
		return ProcessHistory{}, ErrProcessNotFound
	}
	if err != nil {
		return ProcessHistory{}, err
	}
	pts, err := s.ch.ProcessSeries(ctx, clickhouse.ProcSeriesQuery{
		RangeQuery: clickhouse.RangeQuery{OrgID: r.OrgID, NodeID: r.NodeID, From: from, To: to}, Keys: []wire.ProcessKey{r.Key}, Step: out.Step,
	})
	if err != nil {
		return ProcessHistory{}, err
	}
	out.Points = pts[r.Key]
	if now.Sub(to) <= time.Hour {
		f, ok, err := s.ch.ProcessFacts(ctx, r.OrgID, r.NodeID, r.Key, to)
		if err != nil {
			return ProcessHistory{}, err
		}
		if ok {
			out.Facts = &f
		}
	}
	key := clickhouse.ProcStartKey{NodeID: r.NodeID, Key: r.Key}
	starts, err := s.ProcessStarts(ctx, r.OrgID, []clickhouse.ProcStartKey{key}, to)
	if err != nil {
		return ProcessHistory{}, err
	}
	out.Start = starts[key]
	return out, nil
}
