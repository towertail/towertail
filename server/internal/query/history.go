package query

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
)

// HistoryRequest asks for one metric of one node. A zero To means now.
type HistoryRequest struct {
	OrgID, NodeID uuid.UUID
	Metric        string
	From, To      time.Time
	Step          time.Duration
}

// History gives the points of one metric and the bucket of the points
// (0 means one point for each sample). The plan clamps From. The bool is
// true when it did.
func (s *Service) History(ctx context.Context, r HistoryRequest) ([]clickhouse.HistoryPoint, time.Duration, bool, error) {
	if s.ch == nil {
		return nil, 0, false, ErrNoHistory
	}
	now := s.now().UTC()
	from, to, clamped := s.clampRange(ctx, r.OrgID, r.From, r.To, now)
	pts, bucket, err := s.ch.QueryHistory(ctx, clickhouse.HistoryQuery{
		OrgID: r.OrgID, NodeID: r.NodeID, Metric: r.Metric, From: from, To: to, Step: r.Step,
	})
	return pts, bucket, clamped, err
}

// HistoryMultiRequest asks for many metrics of many nodes. A zero To means
// now. With Previous, the result also holds the window of the same length
// that ends at From.
type HistoryMultiRequest struct {
	OrgID    uuid.UUID
	NodeIDs  []uuid.UUID
	Metrics  []string
	From, To time.Time
	Step     time.Duration
	Previous bool
}

// HistoryWindow is a range and the points in it, by node and metric.
type HistoryWindow struct {
	From, To time.Time
	Series   map[uuid.UUID]map[string][]clickhouse.HistoryPoint
}

// HistoryMultiResult is the points of the range. Bucket is the bucket of
// the points. Clamped is true when the plan moved the start of a window.
type HistoryMultiResult struct {
	HistoryWindow
	Bucket   time.Duration
	Clamped  bool
	Previous *HistoryWindow
}

// HistoryMulti gives the points of many metrics of many nodes. The plan
// clamps From. A previous window fully outside the plan has From equal to
// To and no series.
func (s *Service) HistoryMulti(ctx context.Context, r HistoryMultiRequest) (HistoryMultiResult, error) {
	if s.ch == nil {
		return HistoryMultiResult{}, ErrNoHistory
	}
	names := clickhouse.MetricNames()
	for _, m := range r.Metrics {
		if !slices.Contains(names, m) {
			return HistoryMultiResult{}, clickhouse.ErrUnknownMetric
		}
	}
	now := s.now().UTC()
	from, to, clamped := s.clampRange(ctx, r.OrgID, r.From, r.To, now)
	q := clickhouse.HistoryMultiQuery{OrgID: r.OrgID, NodeIDs: r.NodeIDs, Metrics: r.Metrics, From: from, To: to, Step: r.Step}
	series, bucket, err := s.ch.QueryHistoryMulti(ctx, q)
	if err != nil {
		return HistoryMultiResult{}, err
	}
	out := HistoryMultiResult{HistoryWindow: HistoryWindow{From: from, To: to, Series: series}, Bucket: bucket, Clamped: clamped}
	if !r.Previous {
		return out, nil
	}
	span := r.To.Sub(r.From)
	if r.To.IsZero() {
		span = now.Sub(r.From)
	}
	end := r.From
	start, prevClamped := s.Clamp(ctx, r.OrgID, end.Add(-span), now)
	out.Clamped = out.Clamped || prevClamped
	if !start.Before(end) {
		out.Previous = &HistoryWindow{From: start, To: start}
		return out, nil
	}
	q.From, q.To = start, end
	prev, _, err := s.ch.QueryHistoryMulti(ctx, q)
	if err != nil {
		return HistoryMultiResult{}, err
	}
	out.Previous = &HistoryWindow{From: start, To: end, Series: prev}
	return out, nil
}

// HostsByTag gives the nodes of the org that have the tag, with no case.
func (s *Service) HostsByTag(ctx context.Context, orgID uuid.UUID, tag string) ([]store.Node, error) {
	nodes, err := s.nodes.ListNodes(ctx, orgID)
	if err != nil {
		return nil, err
	}
	tag = strings.TrimSpace(tag)
	var out []store.Node
	for _, n := range nodes {
		if slices.ContainsFunc(n.Tags, func(t string) bool { return strings.EqualFold(t, tag) }) {
			out = append(out, n)
		}
	}
	return out, nil
}

// DiskResult is the use of the mounts of a node over a range. The rows
// of Points come in mount order.
type DiskResult struct {
	From, To time.Time
	Bucket   time.Duration
	Clamped  bool
	Points   []clickhouse.DiskPoint
}

// Disks gives the use of each mount of a node. The plan clamps from.
func (s *Service) Disks(ctx context.Context, orgID, nodeID uuid.UUID, from, to time.Time) (DiskResult, error) {
	if s.ch == nil {
		return DiskResult{}, ErrNoHistory
	}
	out := DiskResult{}
	out.From, out.To, out.Clamped = s.clampRange(ctx, orgID, from, to, s.now().UTC())
	pts, bucket, err := s.ch.DiskHistory(ctx, clickhouse.RangeQuery{OrgID: orgID, NodeID: nodeID, From: out.From, To: out.To})
	if err != nil {
		return DiskResult{}, err
	}
	out.Points, out.Bucket = pts, bucket
	return out, nil
}

// clampRange applies the plan limit to from. A zero to means now. To
// never comes before from.
func (s *Service) clampRange(ctx context.Context, orgID uuid.UUID, from, to, now time.Time) (time.Time, time.Time, bool) {
	if to.IsZero() {
		to = now
	}
	from, clamped := s.Clamp(ctx, orgID, from, now)
	if to.Before(from) {
		to = from
	}
	return from, to, clamped
}
