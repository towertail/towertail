package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/google/uuid"
)

// HistoryPoint is a single time-bucket result from a history range
// query. ResValue is the primary value for the requested metric;
// additional aggregates (max, ratio) can be added as needed.
type HistoryPoint struct {
	TS    time.Time
	Value float64
	Max   float64
}

// HistoryQuery parameters. Resolution is picked by the caller based on
// (To - From) span.
type HistoryQuery struct {
	OrgID      uuid.UUID
	NodeID     uuid.UUID
	Metric     string // "cpu" | "mem" | "net_rx" | "net_tx" | "disk_read" | "disk_write"
	From, To   time.Time
	Resolution time.Duration // 1m or 5m align with samples_1m / samples_5m
}

// QueryHistory returns aggregated points for the requested metric +
// time range. Uses the rollup MV matching the resolution. Steps finer
// than one minute fall back to samples_raw.
func (c *Client) QueryHistory(ctx context.Context, q HistoryQuery) ([]HistoryPoint, error) {
	var sql string
	switch {
	case q.Resolution >= 5*time.Minute:
		sql = historySQL("samples_5m", "ts_5m", q.Metric)
	case q.Resolution >= time.Minute:
		sql = historySQL("samples_1m", "ts_minute", q.Metric)
	default:
		sql = rawHistorySQL(q.Metric)
	}

	rows, err := c.conn.Query(ctx, sql, q.OrgID, q.NodeID, q.From, q.To)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []HistoryPoint
	for rows.Next() {
		var p HistoryPoint
		if err := rows.Scan(&p.TS, &p.Value, &p.Max); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func historySQL(view, bucket, metric string) string {
	// All rollup columns are AggregateFunction states — we Merge them.
	switch metric {
	case "cpu":
		return fmt.Sprintf(
			"SELECT %s AS ts, avgMerge(cpu_pct_avg) AS v, maxMerge(cpu_pct_max) AS m "+
				"FROM towertail.%s WHERE org_id = ? AND node_id = ? AND %s BETWEEN ? AND ? "+
				"GROUP BY ts ORDER BY ts",
			bucket, view, bucket,
		)
	case "mem":
		return fmt.Sprintf(
			"SELECT %s AS ts, avgMerge(mem_ratio_avg) AS v, avgMerge(mem_ratio_avg) AS m "+
				"FROM towertail.%s WHERE org_id = ? AND node_id = ? AND %s BETWEEN ? AND ? "+
				"GROUP BY ts ORDER BY ts",
			bucket, view, bucket,
		)
	case "net_rx":
		return fmt.Sprintf(
			"SELECT %s AS ts, avgMerge(net_rx_avg) AS v, avgMerge(net_rx_avg) AS m "+
				"FROM towertail.%s WHERE org_id = ? AND node_id = ? AND %s BETWEEN ? AND ? "+
				"GROUP BY ts ORDER BY ts",
			bucket, view, bucket,
		)
	case "net_tx":
		return fmt.Sprintf(
			"SELECT %s AS ts, avgMerge(net_tx_avg) AS v, avgMerge(net_tx_avg) AS m "+
				"FROM towertail.%s WHERE org_id = ? AND node_id = ? AND %s BETWEEN ? AND ? "+
				"GROUP BY ts ORDER BY ts",
			bucket, view, bucket,
		)
	case "disk_read":
		return fmt.Sprintf(
			"SELECT %s AS ts, avgMerge(disk_read_avg) AS v, avgMerge(disk_read_avg) AS m "+
				"FROM towertail.%s WHERE org_id = ? AND node_id = ? AND %s BETWEEN ? AND ? "+
				"GROUP BY ts ORDER BY ts",
			bucket, view, bucket,
		)
	case "disk_write":
		return fmt.Sprintf(
			"SELECT %s AS ts, avgMerge(disk_write_avg) AS v, avgMerge(disk_write_avg) AS m "+
				"FROM towertail.%s WHERE org_id = ? AND node_id = ? AND %s BETWEEN ? AND ? "+
				"GROUP BY ts ORDER BY ts",
			bucket, view, bucket,
		)
	}
	return ""
}

func rawHistorySQL(metric string) string {
	switch metric {
	case "cpu":
		return "SELECT ts, toFloat64(cpu_pct), toFloat64(cpu_pct) FROM towertail.samples_raw " +
			"WHERE org_id = ? AND node_id = ? AND ts BETWEEN ? AND ? ORDER BY ts"
	case "mem":
		return "SELECT ts, toFloat64(mem_used) / greatest(toFloat64(mem_total), 1), toFloat64(mem_used) / greatest(toFloat64(mem_total), 1) " +
			"FROM towertail.samples_raw WHERE org_id = ? AND node_id = ? AND ts BETWEEN ? AND ? ORDER BY ts"
	case "net_rx":
		return "SELECT ts, toFloat64(net_rx_bps), toFloat64(net_rx_bps) FROM towertail.samples_raw " +
			"WHERE org_id = ? AND node_id = ? AND ts BETWEEN ? AND ? ORDER BY ts"
	case "net_tx":
		return "SELECT ts, toFloat64(net_tx_bps), toFloat64(net_tx_bps) FROM towertail.samples_raw " +
			"WHERE org_id = ? AND node_id = ? AND ts BETWEEN ? AND ? ORDER BY ts"
	case "disk_read":
		return "SELECT ts, toFloat64(disk_read_bps), toFloat64(disk_read_bps) FROM towertail.samples_raw " +
			"WHERE org_id = ? AND node_id = ? AND ts BETWEEN ? AND ? ORDER BY ts"
	case "disk_write":
		return "SELECT ts, toFloat64(disk_write_bps), toFloat64(disk_write_bps) FROM towertail.samples_raw " +
			"WHERE org_id = ? AND node_id = ? AND ts BETWEEN ? AND ? ORDER BY ts"
	}
	return ""
}

// ErrNoAlert is returned when the org has no alert with the id.
var ErrNoAlert = errors.New("clickhouse: alert not found")

// alertKinds are the event kinds that open or close an alert.
const alertKinds = "kind IN ('threshold_crossed', 'reachability_changed')"

// TintRow is the last tint of one (org, node, metric).
type TintRow struct {
	OrgID  uuid.UUID
	NodeID uuid.UUID
	Metric string
	Tint   string
	TS     time.Time
}

// LastTints gives the last tint of each (org, node, metric) with an
// event since the time.
func (c *Client) LastTints(ctx context.Context, since time.Time) ([]TintRow, error) {
	rows, err := c.conn.Query(ctx,
		"SELECT org_id, node_id, metric, toString(argMax(tint, ts)), max(ts) FROM towertail.events "+
			"WHERE ts >= ? AND "+alertKinds+" GROUP BY org_id, node_id, metric", since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TintRow
	for rows.Next() {
		var r TintRow
		if err := rows.Scan(&r.OrgID, &r.NodeID, &r.Metric, &r.Tint, &r.TS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// NodeSeen is the time of the last sample of a node.
type NodeSeen struct {
	OrgID  uuid.UUID
	NodeID uuid.UUID
	TS     time.Time
}

// LastSeen gives the last sample time of each node with a sample since
// the time.
func (c *Client) LastSeen(ctx context.Context, since time.Time) ([]NodeSeen, error) {
	rows, err := c.conn.Query(ctx,
		"SELECT org_id, node_id, max(ts) FROM towertail.samples_raw WHERE ts >= ? GROUP BY org_id, node_id", since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeSeen
	for rows.Next() {
		var r NodeSeen
		if err := rows.Scan(&r.OrgID, &r.NodeID, &r.TS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AlertRow is an open alert: the last event of a (node, metric) when its
// tint is not ok.
type AlertRow struct {
	EventID      uuid.UUID
	NodeID       uuid.UUID
	Kind         string
	Metric       string
	Tint         string
	TS           time.Time
	Payload      string
	AckedBy      *uuid.UUID
	AckedAt      *time.Time
	SnoozedUntil *time.Time
}

// alertWindow is the age limit of open alerts. It is the TTL of events.
const alertWindow = 90 * 24 * time.Hour

const alertSelect = "SELECT event_id, node_id, toString(kind) AS kind_s, metric, toString(tint) AS tint_s, ts, payload, acked_by, acked_at, snoozed_until " +
	"FROM towertail.events WHERE org_id = ? AND ts >= ? AND " + alertKinds + " " +
	"ORDER BY ts DESC LIMIT 1 BY node_id, metric"

// OpenAlerts gives the open alerts of the org, the newest first.
func (c *Client) OpenAlerts(ctx context.Context, orgID uuid.UUID) ([]AlertRow, error) {
	return c.queryAlerts(ctx,
		"SELECT * FROM ("+alertSelect+") WHERE tint_s != 'ok' ORDER BY ts DESC",
		orgID, time.Now().Add(-alertWindow))
}

// Alert gives the open alert with the event id. It returns ErrNoAlert
// when the event is not the open alert of its (node, metric).
func (c *Client) Alert(ctx context.Context, orgID, eventID uuid.UUID) (AlertRow, error) {
	rows, err := c.queryAlerts(ctx,
		"SELECT * FROM ("+alertSelect+") WHERE tint_s != 'ok' AND event_id = ?",
		orgID, time.Now().Add(-alertWindow), eventID)
	if err != nil {
		return AlertRow{}, err
	}
	if len(rows) == 0 {
		return AlertRow{}, ErrNoAlert
	}
	return rows[0], nil
}

func (c *Client) queryAlerts(ctx context.Context, sql string, args ...any) ([]AlertRow, error) {
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRow
	for rows.Next() {
		var r AlertRow
		if err := rows.Scan(&r.EventID, &r.NodeID, &r.Kind, &r.Metric, &r.Tint, &r.TS, &r.Payload,
			&r.AckedBy, &r.AckedAt, &r.SnoozedUntil); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AckAlert records who acked the alert and when.
func (c *Client) AckAlert(ctx context.Context, orgID, eventID, by uuid.UUID, at time.Time) error {
	return c.updateEvent(ctx, "acked_by = ?, acked_at = fromUnixTimestamp64Milli(?)", orgID, eventID, by, at.UnixMilli())
}

// SnoozeAlert records the end of the snooze of the alert.
func (c *Client) SnoozeAlert(ctx context.Context, orgID, eventID uuid.UUID, until time.Time) error {
	return c.updateEvent(ctx, "snoozed_until = fromUnixTimestamp64Milli(?)", orgID, eventID, until.UnixMilli())
}

// updateEvent runs a mutation and waits for it. Acks are rare, so a
// mutation on the small events table is cheap enough.
func (c *Client) updateEvent(ctx context.Context, set string, orgID, eventID uuid.UUID, args ...any) error {
	ctx = ch.Context(ctx, ch.WithSettings(ch.Settings{"mutations_sync": 1}))
	args = append(args, orgID, eventID)
	return c.conn.Exec(ctx, "ALTER TABLE towertail.events UPDATE "+set+" WHERE org_id = ? AND event_id = ?", args...)
}
