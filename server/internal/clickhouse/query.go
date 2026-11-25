package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

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

// tableOf gives the table of the metric family in the tier. The raw tier
// has one table for all the families.
func (t tier) tableOf(family string) string {
	if t == tierRaw {
		return t.table
	}
	return family + strings.TrimPrefix(t.table, "samples")
}

// rank orders the tiers from the finest (0) to the coarsest (3).
func (t tier) rank() int {
	switch t {
	case tierRaw:
		return 0
	case tier1m:
		return 1
	case tier5m:
		return 2
	default:
		return 3
	}
}

// historyTier chooses the table by the length of the range and by the age
// of its start. It returns the coarser of the two choices.
func historyTier(span, age time.Duration) tier {
	const day = 24 * time.Hour
	var bySpan, byAge tier
	switch {
	case span <= 6*time.Hour:
		bySpan = tierRaw
	case span <= 7*day:
		bySpan = tier1m
	case span <= 30*day:
		bySpan = tier5m
	default:
		bySpan = tier1h
	}
	switch {
	case age <= 7*day:
		byAge = tierRaw
	case age <= 30*day:
		byAge = tier1m
	case age <= 90*day:
		byAge = tier5m
	default:
		byAge = tier1h
	}
	if byAge.rank() > bySpan.rank() {
		return byAge
	}
	return bySpan
}

// QueryHistory returns the points of one metric in the range, and the
// bucket of the points (0 means one point for each sample). The tier
// comes from the range, and the step groups the points of the tier.
func (c *Client) QueryHistory(ctx context.Context, q HistoryQuery) ([]HistoryPoint, time.Duration, error) {
	t := historyTier(q.To.Sub(q.From), time.Since(q.From))
	family, err := metricFamily(t, q.Metric)
	if err != nil {
		return nil, 0, err
	}
	sql, err := c.historySQL(t, family, []string{q.Metric}, q.Step, false)
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
		var (
			ts   time.Time
			v, m *float64
		)
		if err := rows.Scan(&ts, &v, &m); err != nil {
			return nil, 0, err
		}
		if v == nil {
			continue
		}
		p := HistoryPoint{TS: ts, Value: *v, Max: *v}
		if m != nil {
			p.Max = *m
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
// node with no points, has no entry. A bucket with no value of a metric
// has no point of that metric. The tier rules are the same as in
// QueryHistory. On a rollup tier, the metrics of each family come from
// their own table, so the query runs once for each family.
func (c *Client) QueryHistoryMulti(ctx context.Context, q HistoryMultiQuery) (map[uuid.UUID]map[string][]HistoryPoint, time.Duration, error) {
	t := historyTier(q.To.Sub(q.From), time.Since(q.From))
	out, err := c.queryTier(ctx, t, q)
	if err != nil {
		return nil, 0, err
	}
	return out, t.resolution(q.Step), nil
}

// MinuteHistory returns the 1 minute rollup of each metric of each node,
// whatever the span of the range. It ignores Step. The result has the same
// shape as the one of QueryHistoryMulti.
func (c *Client) MinuteHistory(ctx context.Context, q HistoryMultiQuery) (map[uuid.UUID]map[string][]HistoryPoint, error) {
	q.Step = 0
	return c.queryTier(ctx, tier1m, q)
}

// queryTier reads the metrics of the query from one tier.
func (c *Client) queryTier(ctx context.Context, t tier, q HistoryMultiQuery) (map[uuid.UUID]map[string][]HistoryPoint, error) {
	out := map[uuid.UUID]map[string][]HistoryPoint{}
	if len(q.Metrics) == 0 || len(q.NodeIDs) == 0 {
		return out, nil
	}
	var families []string
	byFamily := map[string][]string{}
	for _, m := range q.Metrics {
		f, err := metricFamily(t, m)
		if err != nil {
			return nil, err
		}
		if _, ok := byFamily[f]; !ok {
			families = append(families, f)
		}
		byFamily[f] = append(byFamily[f], m)
	}
	for _, f := range families {
		if err := c.queryFamily(ctx, t, f, byFamily[f], q, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// queryFamily runs the query of the metrics of one family and adds the
// points to out.
func (c *Client) queryFamily(ctx context.Context, t tier, family string, metrics []string, q HistoryMultiQuery, out map[uuid.UUID]map[string][]HistoryPoint) error {
	sql, err := c.historySQL(t, family, metrics, q.Step, true)
	if err != nil {
		return err
	}
	rows, err := c.conn.Query(ctx, sql, q.OrgID, q.NodeIDs, q.From, q.To)
	if err != nil {
		return err
	}
	defer rows.Close()

	vals := make([]*float64, 2*len(metrics))
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
			return err
		}
		for i, m := range metrics {
			v := vals[2*i]
			if v == nil {
				continue
			}
			p := HistoryPoint{TS: ts, Value: *v, Max: *v}
			if mx := vals[2*i+1]; mx != nil {
				p.Max = *mx
			}
			series := out[id]
			if series == nil {
				series = make(map[string][]HistoryPoint, len(q.Metrics))
				out[id] = series
			}
			series[m] = append(series[m], p)
		}
	}
	return rows.Err()
}

// resolution is the bucket of the points for the step.
func (t tier) resolution(step time.Duration) time.Duration {
	if step > t.bucket && step >= time.Second {
		return step.Truncate(time.Second)
	}
	return t.bucket
}

// rollupCol is the state columns of a metric in the rollup tables of its
// family. The table is <family>_<tier>. The max column is empty for a
// metric with no max state.
type rollupCol struct {
	family   string
	avg, max string
}

// Rollup families.
const (
	familySamples = "samples"
	familyHealth  = "health"
)

func health(name string) rollupCol { return rollupCol{familyHealth, "avg_" + name, "max_" + name} }

var rollupCols = map[string]rollupCol{
	"cpu":          {familySamples, "cpu_pct_avg", "cpu_pct_max"},
	"mem":          {familySamples, "mem_ratio_avg", ""},
	"net_rx":       {familySamples, "net_rx_avg", ""},
	"net_tx":       {familySamples, "net_tx_avg", ""},
	"disk_read":    {familySamples, "disk_read_avg", ""},
	"disk_write":   {familySamples, "disk_write_avg", ""},
	"load":         health("load_1"),
	"load_1":       health("load_1"),
	"procs":        health("procs"),
	"disk":         health("disk"),
	"inodes":       health("inodes"),
	"zombies":      health("zombies"),
	"pids":         health("pids"),
	"files":        health("files"),
	"psi_cpu":      health("psi_cpu"),
	"psi_mem":      health("psi_mem"),
	"psi_mem_full": health("psi_mem_full"),
	"psi_io":       health("psi_io"),
	"psi_io_full":  health("psi_io_full"),
	"mem_pressure": health("mem_pressure"),
}

// ratioOf is the expression of used/max, or null when max is not positive.
func ratioOf(used, mx string) string {
	return fmt.Sprintf("if(%[2]s > 0, toFloat64(%[1]s) / toFloat64(%[2]s), NULL)", used, mx)
}

// rawExprs are the samples_raw expressions of each metric. A health
// metric is null when the sample has no value.
var rawExprs = map[string]string{
	"cpu":          "toFloat64(cpu_pct)",
	"mem":          "toFloat64(mem_used) / greatest(toFloat64(mem_total), 1)",
	"net_rx":       "toFloat64(net_rx_bps)",
	"net_tx":       "toFloat64(net_tx_bps)",
	"disk_read":    "toFloat64(disk_read_bps)",
	"disk_write":   "toFloat64(disk_write_bps)",
	"load":         "toFloat64(cpu_load_1)",
	"load_1":       "toFloat64(cpu_load_1)",
	"procs":        "toFloat64(procs_total)",
	"disk":         "toFloat64(disk_ratio_max)",
	"inodes":       "toFloat64(inodes_ratio_max)",
	"zombies":      "toFloat64(zombies)",
	"pids":         ratioOf("pids_used", "pids_max"),
	"files":        ratioOf("files_used", "files_max"),
	"psi_cpu":      "toFloat64(psi_cpu_some)",
	"psi_mem":      "toFloat64(psi_mem_some)",
	"psi_mem_full": "toFloat64(psi_mem_full)",
	"psi_io":       "toFloat64(psi_io_some)",
	"psi_io_full":  "toFloat64(psi_io_full)",
	"mem_pressure": "toFloat64(mem_pressure_pct)",
}

// MetricNames gives the sorted names that the history queries accept. It
// includes the alias load.
func MetricNames() []string { return slices.Sorted(maps.Keys(rawExprs)) }

// metricFamily gives the family of the table that has the metric in the
// tier. The raw tier has one table for all the metrics.
func metricFamily(t tier, metric string) (string, error) {
	if t == tierRaw {
		if _, ok := rawExprs[metric]; !ok {
			return "", ErrUnknownMetric
		}
		return familySamples, nil
	}
	rc, ok := rollupCols[metric]
	if !ok {
		if _, raw := rawExprs[metric]; raw {
			return "", ErrNoRollup
		}
		return "", ErrUnknownMetric
	}
	return rc.family, nil
}

// metricExprs gives the value and max expressions of a metric in a tier.
// On the raw tier, group wraps them in avg and max.
func metricExprs(t tier, metric string, group bool) (v, m string, err error) {
	if _, err := metricFamily(t, metric); err != nil {
		return "", "", err
	}
	if t == tierRaw {
		e := rawExprs[metric]
		if group {
			return "avg(" + e + ")", "max(" + e + ")", nil
		}
		return e, e, nil
	}
	rc := rollupCols[metric]
	v = "avgMerge(" + rc.avg + ")"
	m = v
	if rc.max != "" {
		m = "maxMerge(" + rc.max + ")"
	}
	return v, m, nil
}

// historySQL builds the query of the metrics of one family. Each metric
// gives a value and a max column (v0, m0, v1, ...). Both can be null. With
// multiNode, the rows also have the node_id and the node_id filter is a
// list.
func (c *Client) historySQL(t tier, family string, metrics []string, step time.Duration, multiNode bool) (string, error) {
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
		if t != tierRaw && rollupCols[metric].family != family {
			return "", fmt.Errorf("clickhouse: metric %s is not in the %s family", metric, family)
		}
		cols = append(cols, fmt.Sprintf("toNullable(toFloat64(%s)) AS v%d, toNullable(toFloat64(%s)) AS m%d", v, i, m, i))
	}
	sel, where, groupBy, orderBy := "", "node_id = ?", "t", "t"
	if multiNode {
		sel, where, groupBy, orderBy = "node_id, ", "node_id IN ?", "node_id, t", "node_id, t"
	}
	sql := fmt.Sprintf("SELECT %s%s AS t, %s FROM %s WHERE org_id = ? AND %s AND %s BETWEEN ? AND ?",
		sel, ts, strings.Join(cols, ", "), c.table(t.tableOf(family)), where, t.ts)
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

// HostFacts are the static facts of a host from its newest sample.
type HostFacts struct {
	SeenAt            time.Time
	OS, Arch, Kernel  string
	Sampler           string
	Cores             uint16
	MemTotal, UptimeS uint64
}

// HostFacts gives the newest facts of a node in samples_raw since the
// time. The bool is false when the node has no sample since then.
func (c *Client) HostFacts(ctx context.Context, orgID, nodeID uuid.UUID, since time.Time) (HostFacts, bool, error) {
	var (
		f HostFacts
		n uint64
	)
	err := c.conn.QueryRow(ctx,
		"SELECT max(ts), argMax(host_os, ts), argMax(host_arch, ts), argMax(host_kernel, ts), argMax(host_sampler, ts), "+
			"argMax(cpu_cores, ts), argMax(mem_total, ts), argMax(host_uptime_s, ts), count() "+
			"FROM "+c.table("samples_raw")+" WHERE org_id = ? AND node_id = ? AND ts >= ?", orgID, nodeID, since).
		Scan(&f.SeenAt, &f.OS, &f.Arch, &f.Kernel, &f.Sampler, &f.Cores, &f.MemTotal, &f.UptimeS, &n)
	if err != nil || n == 0 {
		return HostFacts{}, false, err
	}
	return f, true, nil
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
// Only the metrics of the samples family are valid (they have no nulls).
func (c *Client) WindowStats(ctx context.Context, q StatsQuery) ([]NodeStat, error) {
	if rollupCols[q.Metric].family != familySamples {
		return nil, ErrUnknownMetric
	}
	t := historyTier(q.To.Sub(q.From), time.Since(q.From))
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
	OrgID   uuid.UUID
	NodeID  uuid.UUID
	Metric  string
	Tint    string
	TS      time.Time
	AlertID uuid.UUID
}

// LastTints gives the last tint of each (org, node, metric) with an
// event since the time.
func (c *Client) LastTints(ctx context.Context, since time.Time) ([]TintRow, error) {
	rows, err := c.conn.Query(ctx,
		"SELECT org_id, node_id, metric, toString(argMax(tint, ts)), max(ts), argMax(alert_id, ts) FROM "+c.table("events")+" "+
			"WHERE ts >= ? AND "+alertKinds+" GROUP BY org_id, node_id, metric", since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TintRow
	for rows.Next() {
		var r TintRow
		if err := rows.Scan(&r.OrgID, &r.NodeID, &r.Metric, &r.Tint, &r.TS, &r.AlertID); err != nil {
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
	EventID uuid.UUID
	// AlertID is the id of the incident, the same for each event of it.
	AlertID      uuid.UUID
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
const alertColumns = "event_id, alert_id, node_id, toString(kind) AS kind_s, metric, toString(tint) AS tint_s, ts, payload, acked_by, acked_at, snoozed_until"

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

// Alert gives the last event of the alert with the id. The id is an alert
// id, or an event id of an old client. The last event can have the tint
// ok, so the caller checks if the alert is open. It returns ErrNoAlert
// when a later alert of the same (node, metric) replaced it.
func (c *Client) Alert(ctx context.Context, orgID, id uuid.UUID) (AlertRow, error) {
	rows, err := c.queryAlerts(ctx,
		"SELECT * FROM ("+c.alertSelect()+") WHERE alert_id = ? OR event_id = ?",
		orgID, time.Now().Add(-alertWindow), id, id)
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
		if err := rows.Scan(&r.EventID, &r.AlertID, &r.NodeID, &r.Kind, &r.Metric, &r.Tint, &r.TS, &r.Payload,
			&r.AckedBy, &r.AckedAt, &r.SnoozedUntil); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// WriteAlertLines appends lines to the alert log.
func (c *Client) WriteAlertLines(ctx context.Context, lines []AlertLine) error {
	if len(lines) == 0 {
		return nil
	}
	batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO "+c.table("alert_log"))
	if err != nil {
		return err
	}
	for _, l := range lines {
		if err := batch.Append(l.TS, l.OrgID, l.NodeID, l.AlertID, l.Kind, l.User, l.Until, l.Text); err != nil {
			return err
		}
	}
	return batch.Send()
}

// markSelect reads the last ack line and the last snooze line of each
// alert. An alert with no line of a pair has a count of 0 for it.
const markSelect = "SELECT org_id, alert_id, " +
	"countIf(kind IN ('acked', 'unacked')), toString(argMaxIf(kind, ts, kind IN ('acked', 'unacked'))), " +
	"argMaxIf(user, ts, kind IN ('acked', 'unacked')), maxIf(ts, kind IN ('acked', 'unacked')), " +
	"countIf(kind IN ('snoozed', 'unsnoozed')), toString(argMaxIf(kind, ts, kind IN ('snoozed', 'unsnoozed'))), " +
	"argMaxIf(user, ts, kind IN ('snoozed', 'unsnoozed')), argMaxIf(until, ts, kind IN ('snoozed', 'unsnoozed')) "

type markKey struct{ org, alert uuid.UUID }

// markState is a mark and the facts if the alert log has lines for it.
type markState struct {
	AlertMark
	ackLine, snoozeLine bool
}

// marks reads the marks of the alert log and of the old columns of
// events. Both tables have the columns org_id, alert_id and ts, so cond
// filters both. An alert with log lines of one pair (ack or snooze)
// ignores the old column of that pair.
func (c *Client) marks(ctx context.Context, cond string, args ...any) ([]AlertMark, error) {
	states := map[markKey]*markState{}
	rows, err := c.conn.Query(ctx, markSelect+"FROM "+c.table("alert_log")+" WHERE "+cond+
		" AND kind IN ('acked', 'unacked', 'snoozed', 'unsnoozed') GROUP BY org_id, alert_id", args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			m                markState
			nAck, nSnooze    uint64
			ackKind, snzKind string
			ackUser, snzUser *uuid.UUID
			ackTS            time.Time
			until            *time.Time
		)
		if err := rows.Scan(&m.OrgID, &m.AlertID, &nAck, &ackKind, &ackUser, &ackTS, &nSnooze, &snzKind, &snzUser, &until); err != nil {
			_ = rows.Close()
			return nil, err
		}
		m.ackLine, m.snoozeLine = nAck > 0, nSnooze > 0
		if m.ackLine && ackKind == "acked" {
			m.AckedBy, m.AckedAt = ackUser, &ackTS
		}
		if m.snoozeLine && snzKind == "snoozed" {
			m.SnoozedUntil, m.SnoozedBy = until, snzUser
		}
		states[markKey{m.OrgID, m.AlertID}] = &m
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}

	old, err := c.conn.Query(ctx, "SELECT org_id, alert_id, acked_by, acked_at, snoozed_until FROM "+c.table("events")+
		" WHERE "+cond+" AND (acked_by IS NOT NULL OR snoozed_until IS NOT NULL) ORDER BY ts", args...)
	if err != nil {
		return nil, err
	}
	defer old.Close()
	for old.Next() {
		var (
			org, alert uuid.UUID
			by         *uuid.UUID
			at, until  *time.Time
		)
		if err := old.Scan(&org, &alert, &by, &at, &until); err != nil {
			return nil, err
		}
		k := markKey{org, alert}
		m := states[k]
		if m == nil {
			m = &markState{AlertMark: AlertMark{OrgID: org, AlertID: alert}}
			states[k] = m
		}
		if by != nil && !m.ackLine {
			m.AckedBy, m.AckedAt = by, at
		}
		if until != nil && !m.snoozeLine {
			m.SnoozedUntil = until
		}
	}
	if err := old.Err(); err != nil {
		return nil, err
	}
	out := make([]AlertMark, 0, len(states))
	for _, m := range states {
		out = append(out, m.AlertMark)
	}
	return out, nil
}

// AlertMarks gives the last ack state and the last snooze state of each
// alert. An alert with no ack and no snooze has no entry.
func (c *Client) AlertMarks(ctx context.Context, orgID uuid.UUID, alertIDs []uuid.UUID) (map[uuid.UUID]AlertMark, error) {
	out := map[uuid.UUID]AlertMark{}
	if len(alertIDs) == 0 {
		return out, nil
	}
	ms, err := c.marks(ctx, "org_id = ? AND alert_id IN ?", orgID, alertIDs)
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		out[m.AlertID] = m
	}
	return out, nil
}

// OpenMarks gives the marks of all orgs for the lines since the time. The
// alerter reads them after a restart.
func (c *Client) OpenMarks(ctx context.Context, since time.Time) ([]AlertMark, error) {
	return c.marks(ctx, "ts >= ?", since)
}
