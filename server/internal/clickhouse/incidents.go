package clickhouse

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// IncidentQuery selects the alerts that overlap [From, To]. NodeIDs
// limits the nodes when it is not empty.
type IncidentQuery struct {
	OrgID    uuid.UUID
	From, To time.Time
	NodeIDs  []uuid.UUID
	Limit    int
}

// IncidentRow is one alert, from its first event to its last.
type IncidentRow struct {
	AlertID, NodeID uuid.UUID
	Kind, Metric    string
	// Tint is the tint of the last event. Severity is the worst tint of
	// all events. An alert that has only ok events has the severity ok.
	Tint, Severity string
	Start, LastTS  time.Time
	// End is the time of the change back to ok. It is nil for an open alert.
	End *time.Time
	// Value is the value of the last event that is not ok.
	Value, Peak float64
	// Payload is the payload of the first event.
	Payload string
	Mark    AlertMark
}

// Incidents gives one row for each alert of the org that overlaps the
// range, the newest start first. An alert from before alert_id has one
// row for each event, so a change back to ok of such an alert is a row
// with the severity ok. truncated is true when there were more than
// Limit rows.
func (c *Client) Incidents(ctx context.Context, q IncidentQuery) (rows []IncidentRow, truncated bool, err error) {
	since := q.From.Add(-alertWindow)
	sql := "SELECT alert_id, any(node_id), any(toString(kind)), any(metric), " +
		"toString(argMax(tint, ts)), " +
		"multiIf(countIf(tint = 'critical') > 0, 'critical', countIf(tint = 'warn') > 0, 'warn', 'ok'), " +
		"min(ts) AS start_ts, max(ts), maxIf(ts, tint = 'ok') AS end_ts, countIf(tint = 'ok') AS n_ok, " +
		"argMaxIf(JSONExtractFloat(payload, 'value'), ts, tint != 'ok'), " +
		"max(greatest(JSONExtractFloat(payload, 'value'), JSONExtractFloat(payload, 'peak'))), " +
		"argMin(payload, ts) " +
		"FROM " + c.table("events") + " WHERE org_id = ? AND ts >= ? AND " + alertKinds
	args := []any{q.OrgID, since}
	if len(q.NodeIDs) > 0 {
		sql += " AND node_id IN ?"
		args = append(args, q.NodeIDs)
	}
	sql += " GROUP BY alert_id HAVING start_ts <= ? AND (n_ok = 0 OR end_ts >= ?) ORDER BY start_ts DESC LIMIT ?"
	args = append(args, q.To, q.From, q.Limit+1)

	res, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, false, err
	}
	defer res.Close()
	for res.Next() {
		var (
			r    IncidentRow
			end  time.Time
			nOK  uint64
			last time.Time
		)
		if err := res.Scan(&r.AlertID, &r.NodeID, &r.Kind, &r.Metric, &r.Tint, &r.Severity,
			&r.Start, &last, &end, &nOK, &r.Value, &r.Peak, &r.Payload); err != nil {
			return nil, false, err
		}
		r.LastTS = last
		if nOK > 0 {
			r.End = &end
		}
		rows = append(rows, r)
	}
	if err := res.Err(); err != nil {
		return nil, false, err
	}
	if len(rows) > q.Limit {
		rows, truncated = rows[:q.Limit], true
	}
	marks, err := c.marks(ctx, "org_id = ? AND ts >= ?", q.OrgID, since)
	if err != nil {
		return nil, false, err
	}
	byAlert := make(map[uuid.UUID]AlertMark, len(marks))
	for _, m := range marks {
		byAlert[m.AlertID] = m
	}
	for i := range rows {
		rows[i].Mark = byAlert[rows[i].AlertID]
	}
	return rows, truncated, nil
}

// AlertsByID gives the last row of each alert of the ids. An id is an
// alert id, or the event id of an old client. The map has the ids that the
// caller gave. An alert that a later alert of the same node and metric
// replaced has no entry.
func (c *Client) AlertsByID(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]AlertRow, error) {
	out := make(map[uuid.UUID]AlertRow, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := c.queryAlerts(ctx,
		"SELECT * FROM ("+c.alertSelect()+") WHERE alert_id IN ? OR event_id IN ?",
		orgID, time.Now().Add(-alertWindow), ids, ids)
	if err != nil {
		return nil, err
	}
	want := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	for _, r := range rows {
		if want[r.AlertID] {
			out[r.AlertID] = r
		}
		if want[r.EventID] {
			out[r.EventID] = r
		}
	}
	return out, nil
}

// AlertEvents gives all events of the alert, the oldest first. The id is
// an alert id, or the event id of an old client. It returns ErrNoAlert
// when the org has no such alert.
func (c *Client) AlertEvents(ctx context.Context, orgID, alertID uuid.UUID) ([]EventRow, error) {
	t := c.table("events")
	rows, err := c.conn.Query(ctx,
		"SELECT ts, org_id, node_id, event_id, alert_id, toString(kind), metric, toString(tint), payload FROM "+t+
			" WHERE org_id = ? AND "+alertKinds+" AND alert_id IN "+
			"(SELECT alert_id FROM "+t+" WHERE org_id = ? AND (alert_id = ? OR event_id = ?)) ORDER BY ts",
		orgID, orgID, alertID, alertID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.TS, &e.OrgID, &e.NodeID, &e.EventID, &e.AlertID, &e.Kind, &e.Metric, &e.Tint, &e.Payload); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNoAlert
	}
	return out, nil
}

// AlertLines gives all lines of the alert log of the alert, the oldest
// first.
func (c *Client) AlertLines(ctx context.Context, orgID, alertID uuid.UUID) ([]AlertLine, error) {
	rows, err := c.conn.Query(ctx,
		"SELECT ts, org_id, node_id, alert_id, toString(kind), user, until, text FROM "+c.table("alert_log")+
			" WHERE org_id = ? AND alert_id = ? ORDER BY ts", orgID, alertID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertLine
	for rows.Next() {
		var l AlertLine
		if err := rows.Scan(&l.TS, &l.OrgID, &l.NodeID, &l.AlertID, &l.Kind, &l.User, &l.Until, &l.Text); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
