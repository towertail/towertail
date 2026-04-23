CREATE MATERIALIZED VIEW IF NOT EXISTS towertail.samples_1m
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMMDD(ts_minute)
ORDER BY (org_id, node_id, ts_minute)
TTL toDateTime(ts_minute) + INTERVAL 30 DAY
AS SELECT
    toStartOfMinute(ts)                  AS ts_minute,
    org_id,
    node_id,
    avgState(cpu_pct)                    AS cpu_pct_avg,
    maxState(cpu_pct)                    AS cpu_pct_max,
    avgState(toFloat64(mem_used) / greatest(toFloat64(mem_total), 1)) AS mem_ratio_avg,
    avgState(net_rx_bps)                 AS net_rx_avg,
    avgState(net_tx_bps)                 AS net_tx_avg,
    avgState(disk_read_bps)              AS disk_read_avg,
    avgState(disk_write_bps)             AS disk_write_avg
FROM towertail.samples_raw
GROUP BY ts_minute, org_id, node_id
;

CREATE MATERIALIZED VIEW IF NOT EXISTS towertail.samples_5m
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMMDD(ts_5m)
ORDER BY (org_id, node_id, ts_5m)
TTL toDateTime(ts_5m) + INTERVAL 90 DAY
AS SELECT
    toStartOfFiveMinute(ts)              AS ts_5m,
    org_id,
    node_id,
    avgState(cpu_pct)                    AS cpu_pct_avg,
    maxState(cpu_pct)                    AS cpu_pct_max,
    avgState(toFloat64(mem_used) / greatest(toFloat64(mem_total), 1)) AS mem_ratio_avg,
    avgState(net_rx_bps)                 AS net_rx_avg,
    avgState(net_tx_bps)                 AS net_tx_avg,
    avgState(disk_read_bps)              AS disk_read_avg,
    avgState(disk_write_bps)             AS disk_write_avg
FROM towertail.samples_raw
GROUP BY ts_5m, org_id, node_id
