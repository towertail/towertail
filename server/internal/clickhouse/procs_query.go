package clickhouse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/wire"
)

// RangeQuery is a node and a time range.
type RangeQuery struct {
	OrgID    uuid.UUID
	NodeID   uuid.UUID
	From, To time.Time
}

// TopProc is one process of a node over a range. A process is a name
// and a cmd, split by repo. Cwd and Parent are empty for ranges that
// the rollups serve.
type TopProc struct {
	NodeID    uuid.UUID
	Name      string
	Cmd       string
	Repo      string
	Cwd       string
	Parent    string
	CmdHash   uint64
	CPUPctAvg float64
	CPUPctMax float64
	RSSMax    uint64
	IOBpsAvg  float64
}

// procsSource chooses the processes table by the length of the range and
// by the age of its start. It returns the coarser of the two choices.
func procsSource(span, age time.Duration) (table, ts string) {
	rank := func(d time.Duration) int {
		switch {
		case d <= time.Hour:
			return 0
		case d <= 7*24*time.Hour:
			return 1
		default:
			return 2
		}
	}
	switch max(rank(span), rank(age)) {
	case 0:
		return "procs_topn", "ts"
	case 1:
		return "procs_top_1m", "ts_minute"
	default:
		return "procs_top_1h", "ts_hour"
	}
}

// ProcOrder is the sort of the top processes.
type ProcOrder string

const (
	ProcByCPU ProcOrder = "cpu"
	ProcByMem ProcOrder = "mem"
	ProcByIO  ProcOrder = "io"
)

// TopProcesses gives the top processes in the range, by average CPU, by
// the highest memory or by the average disk I/O. A NodeID of uuid.Nil
// reads all nodes of the org. On a rollup, the order sorts only the rows
// that the rollup kept.
func (c *Client) TopProcesses(ctx context.Context, q RangeQuery, by ProcOrder, limit int) ([]TopProc, error) {
	table, ts := procsSource(q.To.Sub(q.From), time.Since(q.From))
	avg, mx, rss, hash := "cpu_pct", "cpu_pct", "rss", "cityHash64(cmd)"
	io, cwd, parent, final := "avg(coalesce(read_bps, 0) + coalesce(write_bps, 0))", "any(cwd)", "any(parent)", ""
	if table != "procs_topn" {
		avg, mx, rss, hash, final = "cpu_pct_avg", "cpu_pct_max", "rss_max", "cmd_hash", " FINAL"
		io, cwd, parent = "avg(io_bps_avg)", "''", "''"
	}
	where, args := "org_id = ?", []any{q.OrgID}
	if q.NodeID != uuid.Nil {
		where += " AND node_id = ?"
		args = append(args, q.NodeID)
	}
	order := "p_avg DESC, p_rss DESC"
	switch by {
	case ProcByMem:
		order = "p_rss DESC, p_avg DESC"
	case ProcByIO:
		order = "p_io DESC, p_avg DESC, p_rss DESC"
	}
	stmt := fmt.Sprintf("SELECT node_id, name, any(cmd), repo, %s, %s, %s AS p_hash, avg(%s) AS p_avg, toFloat64(max(%s)), max(%s) AS p_rss, toFloat64(%s) AS p_io FROM %s%s "+
		"WHERE %s AND %s BETWEEN ? AND ? GROUP BY node_id, name, p_hash, repo ORDER BY %s LIMIT ?",
		cwd, parent, hash, avg, mx, rss, io, c.table(table), final, where, ts, order)
	args = append(args, q.From, q.To, limit)
	rows, err := c.conn.Query(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopProc
	for rows.Next() {
		var p TopProc
		if err := rows.Scan(&p.NodeID, &p.Name, &p.Cmd, &p.Repo, &p.Cwd, &p.Parent, &p.CmdHash, &p.CPUPctAvg, &p.CPUPctMax, &p.RSSMax, &p.IOBpsAvg); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProcSeriesQuery asks for the points of some processes. Step is a
// whole number of seconds. Use SeriesStep to get a step that fits the
// table.
type ProcSeriesQuery struct {
	RangeQuery
	Keys []wire.ProcessKey
	Step time.Duration
}

// ProcPoint is one bucket of a process: the average CPU, the highest
// memory and the average disk I/O. TS is the start of the bucket, on a
// multiple of the step since the Unix epoch.
type ProcPoint struct {
	TS     time.Time
	CPUPct float64
	RSS    uint64
	IOBps  float64
}

// SeriesStep gives the step for about n points over the range. It is not
// less than the bucket of the table that the span and the age select.
// It is a multiple of that bucket.
func SeriesStep(span, age time.Duration, n int) time.Duration {
	n = max(n, 1)
	return FitStep(span, age, (span+time.Duration(n)-1)/time.Duration(n))
}

// FitStep raises a step to the bucket of the table that the span and the
// age select, and rounds it up to a multiple of that bucket.
func FitStep(span, age, step time.Duration) time.Duration {
	table, _ := procsSource(span, age)
	bucket := 10 * time.Second
	switch table {
	case "procs_top_1m":
		bucket = time.Minute
	case "procs_top_1h":
		bucket = time.Hour
	}
	return max(bucket, (step+bucket-1)/bucket*bucket)
}

// ProcessSeries gives the points of each key in the range. The span and
// the age of the range select the table, as for TopProcesses. A bucket
// with no row has no point.
func (c *Client) ProcessSeries(ctx context.Context, q ProcSeriesQuery) (map[wire.ProcessKey][]ProcPoint, error) {
	out := make(map[wire.ProcessKey][]ProcPoint, len(q.Keys))
	if len(q.Keys) == 0 {
		return out, nil
	}
	table, ts := procsSource(q.To.Sub(q.From), time.Since(q.From))
	cpu, rss, hash, io, final := "cpu_pct", "rss", "cityHash64(cmd)", "coalesce(read_bps, 0) + coalesce(write_bps, 0)", ""
	if table != "procs_topn" {
		cpu, rss, hash, io, final = "cpu_pct_avg", "rss_max", "cmd_hash", "io_bps_avg", " FINAL"
	}
	step := max(int64(q.Step/time.Second), 1)
	pairs := make([]string, len(q.Keys))
	args := []any{q.OrgID, q.NodeID, q.From, q.To}
	for i, k := range q.Keys {
		pairs[i] = "(?, ?)"
		args = append(args, k.Name, k.CmdHash)
	}
	stmt := fmt.Sprintf("SELECT name, %s AS p_hash, toInt64(intDiv(toUnixTimestamp(%s), %d) * %d) AS b, "+
		"toFloat64(avg(%s)), max(%s), toFloat64(avg(%s)) FROM %s%s "+
		"WHERE org_id = ? AND node_id = ? AND %s BETWEEN ? AND ? AND (name, %s) IN (%s) "+
		"GROUP BY name, p_hash, b ORDER BY b",
		hash, ts, step, step, cpu, rss, io, c.table(table), final, ts, hash, strings.Join(pairs, ", "))
	rows, err := c.conn.Query(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			k wire.ProcessKey
			b int64
			p ProcPoint
		)
		if err := rows.Scan(&k.Name, &k.CmdHash, &b, &p.CPUPct, &p.RSS, &p.IOBps); err != nil {
			return nil, err
		}
		p.TS = time.Unix(b, 0).UTC()
		out[k] = append(out[k], p)
	}
	return out, rows.Err()
}

// ErrNoProcess is returned when no row has the process key in the range.
var ErrNoProcess = errors.New("clickhouse: process not found")

// ProcFacts are the facts of the newest raw row of a process, and its
// listening ports.
type ProcFacts struct {
	SeenAt       time.Time
	PID, PPID    int32
	Parent, User string
	Cwd, Repo    string
	State        string
	Threads      int32
	StartTS      *time.Time
	ListenTCP    []uint16
	ListenUDP    []uint16
}

// ProcessInfo gives the command and the last repo of a process in the
// range. The span and the age of the range select the table.
func (c *Client) ProcessInfo(ctx context.Context, org, node uuid.UUID, key wire.ProcessKey, from, to time.Time) (cmd, repo string, err error) {
	table, ts := procsSource(to.Sub(from), time.Since(from))
	hash, final := "cityHash64(cmd)", ""
	if table != "procs_topn" {
		hash, final = "cmd_hash", " FINAL"
	}
	var n uint64
	err = c.conn.QueryRow(ctx, fmt.Sprintf("SELECT count(), argMax(cmd, %[1]s), argMax(repo, %[1]s) FROM %[2]s%[3]s "+
		"WHERE org_id = ? AND node_id = ? AND %[1]s BETWEEN ? AND ? AND name = ? AND %[4]s = ?",
		ts, c.table(table), final, hash), org, node, from, to, key.Name, key.CmdHash).Scan(&n, &cmd, &repo)
	if err != nil {
		return "", "", err
	}
	if n == 0 {
		return "", "", ErrNoProcess
	}
	return cmd, repo, nil
}

// ProcessFacts gives the facts of the newest raw row of the process at
// or before the time, in the last hour. It is false when there is no
// row. The ports are the newest ports row of the same process (PID and
// name) since the process started.
func (c *Client) ProcessFacts(ctx context.Context, org, node uuid.UUID, key wire.ProcessKey, before time.Time) (ProcFacts, bool, error) {
	var (
		f     ProcFacts
		start time.Time
	)
	err := c.conn.QueryRow(ctx, "SELECT ts, pid, ppid, parent, user_name, cwd, repo, state, threads, start_ts FROM "+c.table("procs_topn")+
		" WHERE org_id = ? AND node_id = ? AND ts <= "+msArg+" AND ts > "+msArg+" AND name = ? AND cityHash64(cmd) = ? ORDER BY ts DESC LIMIT 1",
		org, node, before.UnixMilli(), before.Add(-time.Hour).UnixMilli(), key.Name, key.CmdHash).
		Scan(&f.SeenAt, &f.PID, &f.PPID, &f.Parent, &f.User, &f.Cwd, &f.Repo, &f.State, &f.Threads, &start)
	if errors.Is(err, sql.ErrNoRows) {
		return ProcFacts{}, false, nil
	}
	if err != nil {
		return ProcFacts{}, false, err
	}
	if start.Unix() > 0 {
		f.StartTS = &start
	}
	err = c.conn.QueryRow(ctx, "SELECT listen_tcp, listen_udp FROM "+c.table("proc_ports")+
		" WHERE org_id = ? AND node_id = ? AND pid = ? AND name = ? AND ts <= "+msArg+" AND ts >= "+msArg+" ORDER BY ts DESC LIMIT 1",
		org, node, f.PID, key.Name, before.UnixMilli(), start.UnixMilli()).Scan(&f.ListenTCP, &f.ListenUDP)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ProcFacts{}, false, err
	}
	return f, true, nil
}

// SnapshotQuery asks for the top processes of a node at one time.
type SnapshotQuery struct {
	OrgID, NodeID uuid.UUID
	At            time.Time
	By            ProcOrder
	Limit         int
}

// SnapshotProc is one process of a snapshot. PID, PPID, User, Parent,
// Cwd, State, Threads and StartTS come from raw samples only. CPUPct is
// the average on a rollup, and RSS is the maximum.
type SnapshotProc struct {
	CmdHash      uint64
	PID, PPID    int32
	Name, Cmd    string
	User, Parent string
	Cwd, Repo    string
	State        string
	CPUPct       float64
	CPUPctMax    float64
	RSS          uint64
	IOBps        float64
	Threads      int32
	StartTS      *time.Time
}

// Snapshot is the answer of ProcessesAt. Tier is "raw", "1m" or "1h".
// Bucket is 0 for raw. Total is the process count of a raw sample. Prev
// and Next are the times of the neighbor snapshots.
type Snapshot struct {
	TS         time.Time
	Tier       string
	Bucket     time.Duration
	Total      int
	Prev, Next *time.Time
	Procs      []SnapshotProc
}

// msArg binds a time with millisecond precision. A time.Time argument
// has a precision of 1 second.
const msArg = "fromUnixTimestamp64Milli(?, 'UTC')"

const (
	rawSnapshotWindow = 2 * time.Minute
	maxRollup1m       = 10
	maxRollup1h       = 5
)

// ProcessesAt gives the top processes of a node at one time. The age of
// the time selects the table. An empty bucket gives no processes and no
// error.
func (c *Client) ProcessesAt(ctx context.Context, q SnapshotQuery) (Snapshot, error) {
	table, _ := procsSource(0, time.Since(q.At))
	switch table {
	case "procs_topn":
		return c.rawSnapshot(ctx, q)
	case "procs_top_1m":
		return c.rollupSnapshot(ctx, q, "1m", table, "ts_minute", time.Minute, maxRollup1m)
	}
	return c.rollupSnapshot(ctx, q, "1h", "procs_top_1h", "ts_hour", time.Hour, maxRollup1h)
}

func (c *Client) rawSnapshot(ctx context.Context, q SnapshotQuery) (Snapshot, error) {
	out := Snapshot{Tier: "raw", Procs: []SnapshotProc{}}
	procs := c.table("procs_topn")
	var ts time.Time
	err := c.conn.QueryRow(ctx, "SELECT ts FROM "+procs+" WHERE org_id = ? AND node_id = ? AND ts <= "+msArg+" AND ts > "+msArg+" ORDER BY ts DESC LIMIT 1",
		q.OrgID, q.NodeID, q.At.UnixMilli(), q.At.Add(-rawSnapshotWindow).UnixMilli()).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.TS = ts

	order := "cpu_pct DESC, rss DESC"
	switch q.By {
	case ProcByMem:
		order = "rss DESC, cpu_pct DESC"
	case ProcByIO:
		order = "io DESC, cpu_pct DESC"
	}
	rows, err := c.conn.Query(ctx, "SELECT cityHash64(cmd), pid, ppid, name, cmd, user_name, parent, cwd, repo, state, "+
		"toFloat64(cpu_pct), rss, toFloat64(coalesce(read_bps, 0) + coalesce(write_bps, 0)) AS io, threads, start_ts FROM "+procs+
		" WHERE org_id = ? AND node_id = ? AND ts = "+msArg+" ORDER BY "+order+", pid LIMIT ?",
		q.OrgID, q.NodeID, ts.UnixMilli(), q.Limit)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			p     SnapshotProc
			start time.Time
		)
		if err := rows.Scan(&p.CmdHash, &p.PID, &p.PPID, &p.Name, &p.Cmd, &p.User, &p.Parent, &p.Cwd, &p.Repo, &p.State,
			&p.CPUPct, &p.RSS, &p.IOBps, &p.Threads, &start); err != nil {
			return out, err
		}
		if start.Unix() > 0 {
			p.StartTS = &start
		}
		out.Procs = append(out.Procs, p)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}

	var total uint32
	err = c.conn.QueryRow(ctx, "SELECT procs_total FROM "+c.table("samples_raw")+" WHERE org_id = ? AND node_id = ? AND ts = "+msArg+" LIMIT 1",
		q.OrgID, q.NodeID, ts.UnixMilli()).Scan(&total)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	out.Total = int(total)

	if out.Prev, err = c.neighbor(ctx, procs, q, "ts < "+msArg+" AND ts >= "+msArg, "DESC", ts, ts.Add(-time.Hour)); err != nil {
		return out, err
	}
	out.Next, err = c.neighbor(ctx, procs, q, "ts > "+msArg+" AND ts <= "+msArg, "ASC", ts, ts.Add(time.Hour))
	return out, err
}

// neighbor gives the nearest sample time that the condition selects. The
// condition takes the arguments a and b.
func (c *Client) neighbor(ctx context.Context, table string, q SnapshotQuery, cond, dir string, a, b time.Time) (*time.Time, error) {
	var t time.Time
	err := c.conn.QueryRow(ctx, "SELECT ts FROM "+table+" WHERE org_id = ? AND node_id = ? AND "+cond+" ORDER BY ts "+dir+" LIMIT 1",
		q.OrgID, q.NodeID, a.UnixMilli(), b.UnixMilli()).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (c *Client) rollupSnapshot(ctx context.Context, q SnapshotQuery, tier, table, tsCol string, bucket time.Duration, maxRows int) (Snapshot, error) {
	at := q.At.UTC().Truncate(bucket)
	out := Snapshot{TS: at, Tier: tier, Bucket: bucket, Procs: []SnapshotProc{}}
	prev, next := at.Add(-bucket), at.Add(bucket)
	out.Prev = &prev
	if !next.After(time.Now()) {
		out.Next = &next
	}
	order := "cpu_pct_avg DESC, rss_max DESC"
	switch q.By {
	case ProcByMem:
		order = "rss_max DESC, cpu_pct_avg DESC"
	case ProcByIO:
		order = "io_bps_avg DESC, cpu_pct_avg DESC"
	}
	rows, err := c.conn.Query(ctx, "SELECT cmd_hash, name, cmd, repo, toFloat64(cpu_pct_avg), toFloat64(cpu_pct_max), rss_max, io_bps_avg FROM "+
		c.table(table)+" FINAL WHERE org_id = ? AND node_id = ? AND "+tsCol+" = ? ORDER BY "+order+" LIMIT ?",
		q.OrgID, q.NodeID, at, min(q.Limit, maxRows))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var p SnapshotProc
		if err := rows.Scan(&p.CmdHash, &p.Name, &p.Cmd, &p.Repo, &p.CPUPct, &p.CPUPctMax, &p.RSS, &p.IOBps); err != nil {
			return out, err
		}
		out.Procs = append(out.Procs, p)
	}
	return out, rows.Err()
}

// LatestDiskRatio gives the highest used/total ratio of the mounts of
// each node, from the newest row of each mount since the time.
func (c *Client) LatestDiskRatio(ctx context.Context, orgID uuid.UUID, since time.Time) (map[uuid.UUID]float64, error) {
	rows, err := c.conn.Query(ctx,
		"SELECT node_id, max(ratio) FROM ("+
			"SELECT node_id, mount, toFloat64(argMax(used, ts)) / greatest(toFloat64(argMax(total, ts)), 1) AS ratio "+
			"FROM "+c.table("disks")+" WHERE org_id = ? AND ts >= ? GROUP BY node_id, mount) GROUP BY node_id", orgID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]float64{}
	for rows.Next() {
		var (
			id uuid.UUID
			r  float64
		)
		if err := rows.Scan(&id, &r); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}

// DiskPoint is the use of one mount at the end of one bucket.
type DiskPoint struct {
	Mount string
	FS    string
	TS    time.Time
	Used  uint64
	Total uint64
}

// diskBucket gives about 240 points for each mount, 1 minute at least.
func diskBucket(span time.Duration) time.Duration {
	return max(time.Minute, (span / 240).Truncate(time.Minute))
}

// DiskHistory gives the use of each mount of the node in the range, and
// the bucket of the points.
func (c *Client) DiskHistory(ctx context.Context, q RangeQuery) ([]DiskPoint, time.Duration, error) {
	bucket := diskBucket(q.To.Sub(q.From))
	sql := fmt.Sprintf("SELECT mount, argMax(fs, ts), toStartOfInterval(ts, INTERVAL %d SECOND) AS t, "+
		"argMax(used, ts), argMax(total, ts) FROM %s "+
		"WHERE org_id = ? AND node_id = ? AND ts BETWEEN ? AND ? "+
		"GROUP BY mount, t ORDER BY mount, t", int64(bucket/time.Second), c.table("disks"))
	rows, err := c.conn.Query(ctx, sql, q.OrgID, q.NodeID, q.From, q.To)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []DiskPoint
	for rows.Next() {
		var p DiskPoint
		if err := rows.Scan(&p.Mount, &p.FS, &p.TS, &p.Used, &p.Total); err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, bucket, rows.Err()
}
