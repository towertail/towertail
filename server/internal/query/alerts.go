package query

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/wire"
)

// AlertsRequest asks for the alerts of an org. A NodeID of uuid.Nil
// means all nodes. Days applies to the recent alerts only: 0 gives 7.
type AlertsRequest struct {
	OrgID  uuid.UUID
	Open   bool
	Days   int
	NodeID uuid.UUID
}

// Alerts gives the open alerts, or the recent alerts. The plan clamps
// the start of the recent alerts. A server with no ClickHouse has no
// alerts.
func (s *Service) Alerts(ctx context.Context, r AlertsRequest) (wire.AlertList, error) {
	out := wire.AlertList{Alerts: []wire.Alert{}}
	days := r.Days
	if days == 0 {
		days = defaultAlertDays
	}
	if !r.Open && (days < 1 || days > MaxAlertDays) {
		return out, ErrBadDays
	}
	if s.ch == nil {
		return out, nil
	}
	var rows []clickhouse.AlertRow
	var err error
	if r.Open {
		rows, err = s.ch.OpenAlerts(ctx, r.OrgID)
	} else {
		now := s.now().UTC()
		since, clamped := s.Clamp(ctx, r.OrgID, now.Add(-time.Duration(days)*24*time.Hour), now)
		out.Clamped = clamped
		rows, err = s.ch.RecentAlerts(ctx, r.OrgID, since, RecentAlertsLimit)
	}
	if err != nil {
		return wire.AlertList{}, err
	}
	for _, row := range rows {
		if r.NodeID == uuid.Nil || row.NodeID == r.NodeID {
			out.Alerts = append(out.Alerts, AlertFromRow(row))
		}
	}
	return out, nil
}

// AlertFromRow converts an alert row to its wire form.
func AlertFromRow(r clickhouse.AlertRow) wire.Alert {
	var p struct {
		Value float64 `json:"value"`
	}
	_ = json.Unmarshal([]byte(r.Payload), &p)
	return wire.Alert{
		ID:           r.EventID,
		NodeID:       r.NodeID,
		Kind:         r.Kind,
		Metric:       r.Metric,
		Tint:         wire.Tint(r.Tint),
		TS:           r.TS,
		Value:        p.Value,
		AckedBy:      r.AckedBy,
		AckedAt:      r.AckedAt,
		SnoozedUntil: r.SnoozedUntil,
	}
}
