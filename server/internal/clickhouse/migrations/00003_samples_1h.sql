-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE IF NOT EXISTS {{.DB}}.samples_1h
(
    ts_hour        DateTime('UTC'),
    org_id         UUID,
    node_id        UUID,
    cpu_pct_avg    AggregateFunction(avg, Float32),
    cpu_pct_max    AggregateFunction(max, Float32),
    mem_ratio_avg  AggregateFunction(avg, Float64),
    net_rx_avg     AggregateFunction(avg, UInt64),
    net_tx_avg     AggregateFunction(avg, UInt64),
    disk_read_avg  AggregateFunction(avg, UInt64),
    disk_write_avg AggregateFunction(avg, UInt64)
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(ts_hour)
ORDER BY (org_id, node_id, ts_hour)
TTL ts_hour + INTERVAL 365 DAY;

-- Fill the table from samples_5m (90 days) before the MV starts. The
-- states merge, so the hour averages are exact.
INSERT INTO {{.DB}}.samples_1h
SELECT
    toStartOfHour(ts_5m)            AS ts_hour,
    org_id,
    node_id,
    avgMergeState(cpu_pct_avg)      AS cpu_pct_avg,
    maxMergeState(cpu_pct_max)      AS cpu_pct_max,
    avgMergeState(mem_ratio_avg)    AS mem_ratio_avg,
    avgMergeState(net_rx_avg)       AS net_rx_avg,
    avgMergeState(net_tx_avg)       AS net_tx_avg,
    avgMergeState(disk_read_avg)    AS disk_read_avg,
    avgMergeState(disk_write_avg)   AS disk_write_avg
FROM {{.DB}}.samples_5m
-- A second run after a partial failure must not add the rows again.
WHERE (SELECT count() FROM {{.DB}}.samples_1h) = 0
GROUP BY ts_hour, org_id, node_id;

CREATE MATERIALIZED VIEW IF NOT EXISTS {{.DB}}.samples_1h_mv TO {{.DB}}.samples_1h
AS SELECT
    toStartOfHour(ts)                    AS ts_hour,
    org_id,
    node_id,
    avgState(cpu_pct)                    AS cpu_pct_avg,
    maxState(cpu_pct)                    AS cpu_pct_max,
    avgState(toFloat64(mem_used) / greatest(toFloat64(mem_total), 1)) AS mem_ratio_avg,
    avgState(net_rx_bps)                 AS net_rx_avg,
    avgState(net_tx_bps)                 AS net_tx_avg,
    avgState(disk_read_bps)              AS disk_read_avg,
    avgState(disk_write_bps)             AS disk_write_avg
FROM {{.DB}}.samples_raw
GROUP BY ts_hour, org_id, node_id;
