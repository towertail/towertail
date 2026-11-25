package clickhouse

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
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
	CPUPctAvg float64
	CPUPctMax float64
	RSSMax    uint64
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
)

// TopProcesses gives the top processes in the range, by average CPU or by
// the highest memory. A NodeID of uuid.Nil reads all nodes of the org.
func (c *Client) TopProcesses(ctx context.Context, q RangeQuery, by ProcOrder, limit int) ([]TopProc, error) {
	table, ts := procsSource(q.To.Sub(q.From), time.Since(q.From))
	avg, mx, rss, hash := "cpu_pct", "cpu_pct", "rss", "cityHash64(cmd)"
	cwd, parent, final := "any(cwd)", "any(parent)", ""
	if table != "procs_topn" {
		avg, mx, rss, hash, final = "cpu_pct_avg", "cpu_pct_max", "rss_max", "cmd_hash", " FINAL"
		cwd, parent = "''", "''"
	}
	where, args := "org_id = ?", []any{q.OrgID}
	if q.NodeID != uuid.Nil {
		where += " AND node_id = ?"
		args = append(args, q.NodeID)
	}
	order := "p_avg DESC, p_rss DESC"
	if by == ProcByMem {
		order = "p_rss DESC, p_avg DESC"
	}
	sql := fmt.Sprintf("SELECT node_id, name, any(cmd), repo, %s, %s, avg(%s) AS p_avg, toFloat64(max(%s)), max(%s) AS p_rss FROM %s%s "+
		"WHERE %s AND %s BETWEEN ? AND ? GROUP BY node_id, name, %s, repo ORDER BY %s LIMIT ?",
		cwd, parent, avg, mx, rss, c.table(table), final, where, ts, hash, order)
	args = append(args, q.From, q.To, limit)
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopProc
	for rows.Next() {
		var p TopProc
		if err := rows.Scan(&p.NodeID, &p.Name, &p.Cmd, &p.Repo, &p.Cwd, &p.Parent, &p.CPUPctAvg, &p.CPUPctMax, &p.RSSMax); err != nil {
			return nil, err
		}
		out = append(out, p)
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
