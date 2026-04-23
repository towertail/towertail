package clickhouse

import (
	"context"
	"fmt"
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

// HistoryQuery parameters. Resolution is picked by the caller based on
// (To - From) span.
type HistoryQuery struct {
	OrgID      uuid.UUID
	NodeID     uuid.UUID
	Metric     string        // "cpu" | "mem" | "net_rx" | "net_tx" | "disk_read" | "disk_write"
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
