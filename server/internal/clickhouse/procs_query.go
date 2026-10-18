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
// and a cmd.
type TopProc struct {
	Name      string
	Cmd       string
	CPUPctAvg float64
	CPUPctMax float64
	RSSMax    uint64
}

// procsSource chooses the processes table by the length of the range.
func procsSource(span time.Duration) (table, ts string) {
	switch {
	case span <= time.Hour:
		return "procs_topn", "ts"
	case span <= 7*24*time.Hour:
		return "procs_top_1m", "ts_minute"
	default:
		return "procs_top_1h", "ts_hour"
	}
}

// TopProcesses gives the top processes of the node in the range, by
// average CPU.
func (c *Client) TopProcesses(ctx context.Context, q RangeQuery, limit int) ([]TopProc, error) {
	table, ts := procsSource(q.To.Sub(q.From))
	var sql string
	if table == "procs_topn" {
		sql = fmt.Sprintf("SELECT name, any(cmd), avg(cpu_pct), toFloat64(max(cpu_pct)), max(rss) FROM %s "+
			"WHERE org_id = ? AND node_id = ? AND ts BETWEEN ? AND ? "+
			"GROUP BY name, cityHash64(cmd) ORDER BY 3 DESC LIMIT ?", c.table(table))
	} else {
		sql = fmt.Sprintf("SELECT name, any(cmd), avg(cpu_pct_avg), toFloat64(max(cpu_pct_max)), max(rss_max) FROM %s FINAL "+
			"WHERE org_id = ? AND node_id = ? AND %s BETWEEN ? AND ? "+
			"GROUP BY name, cmd_hash ORDER BY 3 DESC LIMIT ?", c.table(table), ts)
	}
	rows, err := c.conn.Query(ctx, sql, q.OrgID, q.NodeID, q.From, q.To, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopProc
	for rows.Next() {
		var p TopProc
		if err := rows.Scan(&p.Name, &p.Cmd, &p.CPUPctAvg, &p.CPUPctMax, &p.RSSMax); err != nil {
			return nil, err
		}
		out = append(out, p)
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
