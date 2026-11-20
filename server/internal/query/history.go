package query

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
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
