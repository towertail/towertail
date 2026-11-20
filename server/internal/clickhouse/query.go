package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// HistoryQuery parameters.
type HistoryQuery struct {
	OrgID    uuid.UUID
	NodeID   uuid.UUID
	Metric   string // "cpu" | "mem" | "net_rx" | "net_tx" | "disk_read" | "disk_write"
	From, To time.Time
	// Step groups the points. A step smaller than the bucket of the tier
	// gives the points of the tier.
	Step time.Duration
}

// ErrUnknownMetric is returned for a metric that has no column.
var ErrUnknownMetric = errors.New("clickhouse: unknown metric")

// tier is one table of the history, with its time column and bucket.
type tier struct {
	table  string
	ts     string
	bucket time.Duration
}

var (
	tierRaw = tier{"samples_raw", "ts", 0}
	tier1m  = tier{"samples_1m", "ts_minute", time.Minute}
	tier5m  = tier{"samples_5m", "ts_5m", 5 * time.Minute}
	tier1h  = tier{"samples_1h", "ts_hour", time.Hour}
)

// historyTier chooses the table by the length of the range.
func historyTier(span time.Duration) tier {
	switch {
	case span <= 6*time.Hour:
		return tierRaw
	case span <= 7*24*time.Hour:
		return tier1m
	case span <= 30*24*time.Hour:
		return tier5m
	default:
		return tier1h
	}
}

// QueryHistory returns the points of one metric in the range, and the
// bucket of the points (0 means one point for each sample). The tier
// comes from the range, and the step groups the points of the tier.
func (c *Client) QueryHistory(ctx context.Context, q HistoryQuery) ([]HistoryPoint, time.Duration, error) {
	t := historyTier(q.To.Sub(q.From))
	sql, err := c.historySQL(t, []string{q.Metric}, q.Step, false)
	if err != nil {
		return nil, 0, err
	}
	rows, err := c.conn.Query(ctx, sql, q.OrgID, q.NodeID, q.From, q.To)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []HistoryPoint
	for rows.Next() {
		var p HistoryPoint
		if err := rows.Scan(&p.TS, &p.Value, &p.Max); err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, t.resolution(q.Step), rows.Err()
}

// HistoryMultiQuery asks for many metrics of many nodes of one org.
type HistoryMultiQuery struct {
	OrgID    uuid.UUID
	NodeIDs  []uuid.UUID
	Metrics  []string
	From, To time.Time
	Step     time.Duration
}

// ErrNoRollup is returned for a metric that has only raw samples, when
// the range needs a rollup table.
var ErrNoRollup = errors.New("clickhouse: metric has no rollup for this range")

// QueryHistoryMulti returns the points of each metric of each node in
// the range, and the bucket of the points. A node of another org, or a
// node with no points, has no entry. The tier rules are the same as in
// QueryHistory.
func (c *Client) QueryHistoryMulti(ctx context.Context, q HistoryMultiQuery) (map[uuid.UUID]map[string][]HistoryPoint, time.Duration, error) {
	t := historyTier(q.To.Sub(q.From))
	if len(q.Metrics) == 0 || len(q.NodeIDs) == 0 {
		return map[uuid.UUID]map[string][]HistoryPoint{}, t.resolution(q.Step), nil
	}
	sql, err := c.historySQL(t, q.Metrics, q.Step, true)
	if err != nil {
		return nil, 0, err
	}
	rows, err := c.conn.Query(ctx, sql, q.OrgID, q.NodeIDs, q.From, q.To)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := map[uuid.UUID]map[string][]HistoryPoint{}
	vals := make([]float64, 2*len(q.Metrics))
	for rows.Next() {
		var (
			id uuid.UUID
			ts time.Time
		)
		dest := []any{&id, &ts}
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, 0, err
		}
		series := out[id]
		if series == nil {
			series = make(map[string][]HistoryPoint, len(q.Metrics))
			out[id] = series
		}
		for i, m := range q.Metrics {
			series[m] = append(series[m], HistoryPoint{TS: ts, Value: vals[2*i], Max: vals[2*i+1]})
		}
	}
	return out, t.resolution(q.Step), rows.Err()
}

// resolution is the bucket of the points for the step.
func (t tier) resolution(step time.Duration) time.Duration {
	if step > t.bucket && step >= time.Second {
		return step.Truncate(time.Second)
	}
	return t.bucket
}

// rollupCols are the avg and max state columns of each metric.
var rollupCols = map[string][2]string{
	"cpu":        {"cpu_pct_avg", "cpu_pct_max"},
	"mem":        {"mem_ratio_avg", ""},
	"net_rx":     {"net_rx_avg", ""},
	"net_tx":     {"net_tx_avg", ""},
	"disk_read":  {"disk_read_avg", ""},
	"disk_write": {"disk_write_avg", ""},
}

// rawExprs are the samples_raw expressions of each metric.
var rawExprs = map[string]string{
	"cpu":        "toFloat64(cpu_pct)",
	"mem":        "toFloat64(mem_used) / greatest(toFloat64(mem_total), 1)",
	"net_rx":     "toFloat64(net_rx_bps)",
	"net_tx":     "toFloat64(net_tx_bps)",
	"disk_read":  "toFloat64(disk_read_bps)",
	"disk_write": "toFloat64(disk_write_bps)",
	"procs":      "toFloat64(procs_total)",
}

// metricExprs gives the value and max expressions of a metric in a tier.
// On the raw tier, group wraps them in avg and max.
func metricExprs(t tier, metric string, group bool) (v, m string, err error) {
	if t == tierRaw {
		e, ok := rawExprs[metric]
		if !ok {
			return "", "", ErrUnknownMetric
		}
		if group {
			return "avg(" + e + ")", "max(" + e + ")", nil
		}
		return e, e, nil
	}
	rc, ok := rollupCols[metric]
	if !ok {
		if _, raw := rawExprs[metric]; raw {
			return "", "", ErrNoRollup
		}
		return "", "", ErrUnknownMetric
	}
	v = "avgMerge(" + rc[0] + ")"
	m = v
	if rc[1] != "" {
		m = "maxMerge(" + rc[1] + ")"
	}
	return v, m, nil
}

// historySQL builds the query of the metrics. Each metric gives a value
// and a max column (v0, m0, v1, ...). With multiNode, the rows also have
// the node_id and the node_id filter is a list.
func (c *Client) historySQL(t tier, metrics []string, step time.Duration, multiNode bool) (string, error) {
	ts := t.ts
	res := t.resolution(step)
	group := res > t.bucket
	if group {
		ts = fmt.Sprintf("toStartOfInterval(%s, INTERVAL %d SECOND)", t.ts, int64(res/time.Second))
	}
	if t != tierRaw {
		group = true
	}
	cols := make([]string, 0, 2*len(metrics))
	for i, metric := range metrics {
		v, m, err := metricExprs(t, metric, group)
		if err != nil {
			return "", err
		}
		cols = append(cols, fmt.Sprintf("toFloat64(%s) AS v%d, toFloat64(%s) AS m%d", v, i, m, i))
	}
	sel, where, groupBy, orderBy := "", "node_id = ?", "t", "t"
	if multiNode {
		sel, where, groupBy, orderBy = "node_id, ", "node_id IN ?", "node_id, t", "node_id, t"
	}
	sql := fmt.Sprintf("SELECT %s%s AS t, %s FROM %s WHERE org_id = ? AND %s AND %s BETWEEN ? AND ?",
		sel, ts, strings.Join(cols, ", "), c.table(t.table), where, t.ts)
	if group {
		sql += " GROUP BY " + groupBy
	}
	return sql + " ORDER BY " + orderBy, nil
}

// NodeLatest is the newest sample values of a node.
type NodeLatest struct {
	NodeID   uuid.UUID
	TS       time.Time
	CPUPct   float64
	MemRatio float64
	Load1    float64
	NetRxBps uint64
	NetTxBps uint64
}

// LatestByNode gives the newest sample of each node of the org that has a
// sample since the time.
func (c *Client) LatestByNode(ctx context.Context, orgID uuid.UUID, since time.Time) ([]NodeLatest, error) {
	rows, err := c.conn.Query(ctx,
		"SELECT node_id, max(ts), toFloat64(argMax(cpu_pct, ts)), "+
			"toFloat64(argMax(mem_used, ts)) / greatest(toFloat64(argMax(mem_total, ts)), 1), "+
			"toFloat64(argMax(cpu_load_1, ts)), argMax(net_rx_bps, ts), argMax(net_tx_bps, ts) "+
			"FROM "+c.table("samples_raw")+" WHERE org_id = ? AND ts >= ? GROUP BY node_id ORDER BY node_id", orgID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeLatest
	for rows.Next() {
		var n NodeLatest
		if err := rows.Scan(&n.NodeID, &n.TS, &n.CPUPct, &n.MemRatio, &n.Load1, &n.NetRxBps, &n.NetTxBps); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// StatsQuery is a metric over a range. A NodeID of uuid.Nil means all
// nodes of the org.
type StatsQuery struct {
	OrgID    uuid.UUID
	NodeID   uuid.UUID
	Metric   string
	From, To time.Time
}

// NodeStat is the average and the highest value of a metric of a node.
type NodeStat struct {
	NodeID uuid.UUID
	Avg    float64
	Max    float64
	Points int
}

// WindowStats gives the average and the highest value of the metric of
// each node in the range. The tier comes from the range. On a rollup
// tier, Avg is the mean of the bucket averages, and Max is the highest
// bucket max (the highest bucket average for a metric with no max state).
func (c *Client) WindowStats(ctx context.Context, q StatsQuery) ([]NodeStat, error) {
	t := historyTier(q.To.Sub(q.From))
	v, m, err := metricExprs(t, q.Metric, true)
	if err != nil {
		return nil, err
	}
	where, args := "org_id = ?", []any{q.OrgID}
	if q.NodeID != uuid.Nil {
		where += " AND node_id = ?"
		args = append(args, q.NodeID)
	}
	where += " AND " + t.ts + " BETWEEN ? AND ?"
	args = append(args, q.From, q.To)
	var sql string
	if t == tierRaw {
		sql = fmt.Sprintf("SELECT node_id, toFloat64(%s), toFloat64(%s), count() FROM %s WHERE %s GROUP BY node_id ORDER BY node_id",
			v, m, c.table(t.table), where)
	} else {
		sql = fmt.Sprintf("SELECT node_id, toFloat64(avg(v)), toFloat64(max(m)), count() FROM "+
			"(SELECT node_id, %s AS v, %s AS m FROM %s WHERE %s GROUP BY node_id, %s) GROUP BY node_id ORDER BY node_id",
			v, m, c.table(t.table), where, t.ts)
	}
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeStat
	for rows.Next() {
		var (
			s NodeStat
			n uint64
		)
		if err := rows.Scan(&s.NodeID, &s.Avg, &s.Max, &n); err != nil {
			return nil, err
		}
		s.Points = int(n)
		out = append(out, s)
	}
	return out, rows.Err()
}

// ErrNoAlert is returned when the org has no alert with the id.
var ErrNoAlert = errors.New("clickhouse: alert not found")

// alertKinds are the event kinds that open or close an alert.
const alertKinds = "kind IN ('threshold_crossed', 'reachability_changed', 'long_process')"

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
		"SELECT org_id, node_id, metric, toString(argMax(tint, ts)), max(ts) FROM "+c.table("events")+" "+
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
		"SELECT org_id, node_id, max(ts) FROM "+c.table("samples_raw")+" WHERE ts >= ? GROUP BY org_id, node_id", since)
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

// Registrations gives the last registration time of each node with a
// registered event since the time.
func (c *Client) Registrations(ctx context.Context, since time.Time) ([]NodeSeen, error) {
	rows, err := c.conn.Query(ctx,
		"SELECT org_id, node_id, max(ts) FROM "+c.table("events")+" WHERE ts >= ? AND kind = 'registered' GROUP BY org_id, node_id", since)
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

// alertColumns is the column order that queryAlerts scans.
const alertColumns = "event_id, node_id, toString(kind) AS kind_s, metric, toString(tint) AS tint_s, ts, payload, acked_by, acked_at, snoozed_until"

// alertSelect gives the last event of each (node, metric) of the org.
func (c *Client) alertSelect() string {
	return "SELECT " + alertColumns + " " +
		"FROM " + c.table("events") + " WHERE org_id = ? AND ts >= ? AND " + alertKinds + " " +
		"ORDER BY ts DESC LIMIT 1 BY node_id, metric"
}

// OpenAlerts gives the open alerts of the org, the newest first.
func (c *Client) OpenAlerts(ctx context.Context, orgID uuid.UUID) ([]AlertRow, error) {
	return c.queryAlerts(ctx,
		"SELECT * FROM ("+c.alertSelect()+") WHERE tint_s != 'ok' ORDER BY ts DESC",
		orgID, time.Now().Add(-alertWindow))
}

// RecentAlerts gives the alert events of the org since the time, the
// newest first. A change back to ok is also an event.
func (c *Client) RecentAlerts(ctx context.Context, orgID uuid.UUID, since time.Time, limit int) ([]AlertRow, error) {
	return c.queryAlerts(ctx,
		"SELECT "+alertColumns+" FROM "+c.table("events")+" WHERE org_id = ? AND ts >= ? AND "+alertKinds+" ORDER BY ts DESC LIMIT ?",
		orgID, since, limit)
}

// Alert gives the open alert with the event id. It returns ErrNoAlert
// when the event is not the open alert of its (node, metric).
func (c *Client) Alert(ctx context.Context, orgID, eventID uuid.UUID) (AlertRow, error) {
	rows, err := c.queryAlerts(ctx,
		"SELECT * FROM ("+c.alertSelect()+") WHERE tint_s != 'ok' AND event_id = ?",
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
	return c.conn.Exec(ctx, "ALTER TABLE "+c.table("events")+" UPDATE "+set+" WHERE org_id = ? AND event_id = ?", args...)
}
