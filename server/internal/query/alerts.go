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
	var keep []clickhouse.AlertRow
	var ids []uuid.UUID
	for _, row := range rows {
		if r.NodeID == uuid.Nil || row.NodeID == r.NodeID {
			keep = append(keep, row)
			ids = append(ids, alertID(row))
		}
	}
	marks, err := s.ch.AlertMarks(ctx, r.OrgID, ids)
	if err != nil {
		return wire.AlertList{}, err
	}
	for i, row := range keep {
		out.Alerts = append(out.Alerts, AlertFromRow(row, marks[ids[i]]))
	}
	return out, nil
}

// alertPayload holds the keys of the stored event payload that the
// alert list uses (docs/wire.md, Alert).
type alertPayload struct {
	Value     float64    `json:"value"`
	PID       int32      `json:"pid"`
	Name      string     `json:"name"`
	Cmd       string     `json:"cmd"`
	StartTS   *time.Time `json:"start_ts"`
	HighSince *time.Time `json:"high_since"`
	CPUPct    float64    `json:"cpu_pct"`
	Cwd       string     `json:"cwd"`
	Repo      string     `json:"repo"`
	Parent    string     `json:"parent"`
}

// proc gives the process of a long_process payload, or nil when the
// payload has none.
func (p alertPayload) proc() *wire.AlertProc {
	if p.Name == "" && p.PID == 0 {
		return nil
	}
	return &wire.AlertProc{
		PID:       p.PID,
		Name:      p.Name,
		Cmd:       p.Cmd,
		StartTS:   p.StartTS,
		HighSince: p.HighSince,
		CPUPct:    p.CPUPct,
		Cwd:       p.Cwd,
		Repo:      p.Repo,
		Parent:    p.Parent,
	}
}

// alertID is the alert id of the row. A row with no alert id has the id of
// its event.
func alertID(r clickhouse.AlertRow) uuid.UUID {
	if r.AlertID != uuid.Nil {
		return r.AlertID
	}
	return r.EventID
}

// AlertFromRow converts an alert row and the mark of its alert to the wire
// form.
func AlertFromRow(r clickhouse.AlertRow, m clickhouse.AlertMark) wire.Alert {
	var p alertPayload
	_ = json.Unmarshal([]byte(r.Payload), &p)
	a := wire.Alert{
		ID:           alertID(r),
		NodeID:       r.NodeID,
		Kind:         r.Kind,
		Metric:       r.Metric,
		Tint:         wire.Tint(r.Tint),
		TS:           r.TS,
		Value:        p.Value,
		AckedBy:      m.AckedBy,
		AckedAt:      m.AckedAt,
		SnoozedUntil: m.SnoozedUntil,
	}
	if r.Kind == "long_process" {
		a.Proc = p.proc()
	}
	return a
}
