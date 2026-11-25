package query

import (
	"context"
	"encoding/json"
	"slices"
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
		Key:       wire.NewProcessKey(p.Name, p.Cmd),
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

// ListAlerts gives the page of the alerts that overlap the range, with
// the counts of the tabs, the facets and the bins of the starts. It reads
// at most MaxIncidents alerts. A server with no ClickHouse has no alerts.
func (s *Service) ListAlerts(ctx context.Context, q AlertQuery) (wire.AlertPage, error) {
	clamped, err := q.Validate()
	if err != nil {
		return wire.AlertPage{}, err
	}
	now := q.Now
	if now.IsZero() {
		now = s.now().UTC()
		q.Now = now
	}
	var plan bool
	q.From, plan = s.Clamp(ctx, q.OrgID, q.From, now)
	out := wire.AlertPage{Alerts: []wire.Alert{}, Clamped: clamped || plan, From: q.From, To: q.To}
	if s.ch == nil || !q.From.Before(q.To) {
		return out, nil
	}
	set, truncated, err := s.loadAlerts(ctx, q)
	if err != nil {
		return wire.AlertPage{}, err
	}
	out.Truncated = truncated
	out.Counts = set.counts(q)
	if len(q.Facets) > 0 {
		f := set.facets(q)
		out.Facets = &f
	}
	out.Bins = set.bins(q)
	match := set.filter(q, "").sorted(q)
	out.Total = len(match)
	out.Alerts = match[:min(len(match), q.Limit)]
	return out, nil
}

// loadAlerts reads the incidents of a valid query and builds the alert set
// that overlaps the range. It reports whether the read hit MaxIncidents.
// The query has the clamped From and a set Now.
func (s *Service) loadAlerts(ctx context.Context, q AlertQuery) (alertSet, bool, error) {
	iq := clickhouse.IncidentQuery{OrgID: q.OrgID, From: q.From, To: q.To, Limit: MaxIncidents}
	// The host facet counts the other hosts, so it needs all nodes.
	if !slices.Contains(q.Facets, "host") {
		iq.NodeIDs = q.Hosts
	}
	rows, truncated, err := s.ch.Incidents(ctx, iq)
	if err != nil {
		return nil, false, err
	}
	names, err := s.nodeNames(ctx, q.OrgID)
	if err != nil {
		return nil, false, err
	}
	var live map[uuid.UUID]float64
	if s.peaks != nil {
		live = s.peaks.Peaks(q.OrgID)
	}
	set := make(alertSet, 0, len(rows))
	for _, r := range rows {
		set = append(set, alertFromIncident(r, names[r.NodeID], live[r.AlertID], q.Now))
	}
	set = slices.DeleteFunc(set.closeSuperseded(), func(a wire.Alert) bool {
		// An old alert has a row for its change back to ok. It only closes
		// the alert before it.
		return a.Severity == wire.TintOK || a.Start.After(q.To) || (a.End != nil && a.End.Before(q.From))
	})
	return set, truncated, nil
}

// nodeNames gives the display name of each node of the org.
func (s *Service) nodeNames(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	if s.nodes == nil {
		return out, nil
	}
	nodes, err := s.nodes.ListNodes(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		out[n.ID] = n.DisplayName
	}
	return out, nil
}

// alertState is open, acked, snoozed or resolved.
func alertState(a wire.Alert, now time.Time) string {
	switch {
	case a.End != nil:
		return alertResolved
	case a.AckedBy != nil:
		return alertAcked
	case a.SnoozedUntil != nil && a.SnoozedUntil.After(now):
		return alertSnoozed
	}
	return alertOpen
}

// alertFromIncident converts an incident to the wire form. peak is the
// live peak of an open alert, from the memory of the alerter.
func alertFromIncident(r clickhouse.IncidentRow, nodeName string, peak float64, now time.Time) wire.Alert {
	a := AlertFromRow(clickhouse.AlertRow{
		EventID: r.AlertID, AlertID: r.AlertID, NodeID: r.NodeID, Kind: r.Kind,
		Metric: r.Metric, Tint: r.Tint, TS: r.LastTS, Payload: r.Payload,
	}, r.Mark)
	start := r.Start
	a.Start, a.End, a.Value = &start, r.End, r.Value
	a.Severity, a.NodeName = wire.Tint(r.Severity), nodeName
	if p := max(r.Peak, peak); p > 0 {
		a.Peak = &p
	}
	a.State = alertState(a, now)
	return a
}

// reasonMuted is the reason of the change to ok of a muted alert.
const reasonMuted = "muted"

// eventPayload holds the keys of the stored event payload that the detail
// of an alert reads.
type eventPayload struct {
	Value    float64 `json:"value"`
	Peak     float64 `json:"peak"`
	Warn     float64 `json:"warn"`
	Critical float64 `json:"critical"`
	Closed   string  `json:"closed"`
	Reason   string  `json:"reason"`
}

// AlertDetail gives one alert with its limits and its timeline. The id is
// an alert id, or the event id of an old client. It returns
// clickhouse.ErrNoAlert when the org has no such alert.
func (s *Service) AlertDetail(ctx context.Context, orgID, id uuid.UUID) (wire.AlertDetail, error) {
	if s.ch == nil {
		return wire.AlertDetail{}, clickhouse.ErrNoAlert
	}
	events, err := s.ch.AlertEvents(ctx, orgID, id)
	if err != nil {
		return wire.AlertDetail{}, err
	}
	alert := events[0].AlertID
	if alert == uuid.Nil {
		alert = events[0].EventID
	}
	lines, err := s.ch.AlertLines(ctx, orgID, alert)
	if err != nil {
		return wire.AlertDetail{}, err
	}
	marks, err := s.ch.AlertMarks(ctx, orgID, []uuid.UUID{alert})
	if err != nil {
		return wire.AlertDetail{}, err
	}
	names, err := s.nodeNames(ctx, orgID)
	if err != nil {
		return wire.AlertDetail{}, err
	}
	first, last := events[0], events[len(events)-1]
	inc := clickhouse.IncidentRow{
		AlertID: alert, NodeID: first.NodeID, Kind: first.Kind, Metric: first.Metric,
		Tint: last.Tint, Severity: string(wire.TintOK), Start: first.TS, LastTS: last.TS,
		Payload: first.Payload, Mark: marks[alert],
	}
	detail := wire.AlertDetail{Timeline: []wire.AlertLine{}}
	closedLine := slices.ContainsFunc(lines, func(l clickhouse.AlertLine) bool { return l.Kind == "closed" })
	for i, e := range events {
		var p eventPayload
		_ = json.Unmarshal([]byte(e.Payload), &p)
		inc.Peak = max(inc.Peak, p.Value, p.Peak)
		if detail.Limits == nil && p.Critical > 0 {
			detail.Limits = &wire.AlertLimits{Warn: p.Warn, Critical: p.Critical}
		}
		line := wire.AlertLine{TS: e.TS, Tint: wire.Tint(e.Tint)}
		switch {
		case e.Tint == string(wire.TintOK):
			end := e.TS
			inc.End = &end
			line.Kind = "recovered"
			if p.Closed != "" || p.Reason == reasonMuted {
				line.Kind, line.Text = "closed", closeText(p)
			}
		default:
			inc.Value = p.Value
			if e.Tint == string(wire.TintCritical) || inc.Severity == string(wire.TintOK) {
				inc.Severity = e.Tint
			}
			line.Kind = "changed"
			if i == 0 {
				line.Kind = "opened"
			}
		}
		if line.Kind == "closed" && closedLine {
			continue
		}
		detail.Timeline = append(detail.Timeline, line)
	}
	if inc.End == nil {
		// A later alert of the same node and metric replaced an old alert.
		if found, err := s.ch.AlertsByID(ctx, orgID, []uuid.UUID{alert}); err != nil {
			return wire.AlertDetail{}, err
		} else if _, ok := found[alert]; !ok {
			end := last.TS
			inc.End = &end
		}
	}
	for _, l := range lines {
		detail.Timeline = append(detail.Timeline, wire.AlertLine{TS: l.TS, Kind: l.Kind, User: l.User, Until: l.Until, Text: l.Text})
	}
	slices.SortStableFunc(detail.Timeline, func(a, b wire.AlertLine) int { return a.TS.Compare(b.TS) })

	var live float64
	if s.peaks != nil {
		live = s.peaks.Peaks(orgID)[alert]
	}
	detail.Alert = alertFromIncident(inc, names[inc.NodeID], live, s.now().UTC())
	return detail, nil
}

// closeText is the text of a change back to ok that no one asked for.
func closeText(p eventPayload) string {
	if p.Closed == "ignored" {
		return "Ignored"
	}
	return "Muted"
}
