-- +goose NO TRANSACTION
-- +goose Up
-- The schema before goose (files 0001 to 0006). For a database with
-- these tables and no version table, the runner marks this version as
-- applied.

CREATE TABLE IF NOT EXISTS {{.DB}}.samples_raw
(
    ts              DateTime64(3, 'UTC')  CODEC(Delta, ZSTD),
    org_id          UUID,
    node_id         UUID,
    schema_v        UInt8,
    host_name       LowCardinality(String),
    host_os         LowCardinality(String),
    host_arch       LowCardinality(String),
    host_kernel     LowCardinality(String),
    host_uptime_s   UInt64,
    host_sampler    LowCardinality(String),
    host_machine_id String,

    cpu_pct         Float32,
    cpu_load_1      Float32,
    cpu_load_5      Float32,
    cpu_load_15     Float32,
    cpu_cores       UInt16,

    mem_used        UInt64,
    mem_total       UInt64,
    swap_used       UInt64,
    swap_total      UInt64,

    disk_read_bps   UInt64,
    disk_write_bps  UInt64,
    disk_read_cum   UInt64,
    disk_write_cum  UInt64,

    net_rx_bps      UInt64,
    net_tx_bps      UInt64,
    net_rx_cum      UInt64,
    net_tx_cum      UInt64,

    procs_total     UInt32,
    procs_visible   UInt32,
    procs_root      UInt8,

    errors          Array(String),
    ingested_at     DateTime64(3, 'UTC') DEFAULT now64(3)
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (org_id, node_id, ts)
TTL toDateTime(ts) + INTERVAL 7 DAY
SETTINGS index_granularity = 8192;

CREATE TABLE IF NOT EXISTS {{.DB}}.disks
(
    ts      DateTime64(3, 'UTC') CODEC(Delta, ZSTD),
    org_id  UUID,
    node_id UUID,
    mount   LowCardinality(String),
    fs      LowCardinality(String),
    used    UInt64,
    total   UInt64
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (org_id, node_id, mount, ts)
TTL toDateTime(ts) + INTERVAL 7 DAY;

CREATE TABLE IF NOT EXISTS {{.DB}}.disk_io_devices
(
    ts        DateTime64(3, 'UTC') CODEC(Delta, ZSTD),
    org_id    UUID,
    node_id   UUID,
    device    LowCardinality(String),
    read_bps  UInt64,
    write_bps UInt64,
    read_cum  UInt64,
    write_cum UInt64
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (org_id, node_id, device, ts)
TTL toDateTime(ts) + INTERVAL 7 DAY;

CREATE TABLE IF NOT EXISTS {{.DB}}.procs_topn
(
    ts          DateTime64(3, 'UTC') CODEC(Delta, ZSTD),
    org_id      UUID,
    node_id     UUID,
    pid         Int32,
    ppid        Int32,
    name        LowCardinality(String),
    cmd         String         CODEC(ZSTD(3)),
    user_name   LowCardinality(String),
    cpu_pct     Float32,
    rss         UInt64,
    threads     Int32,
    state       LowCardinality(String),
    start_ts    DateTime64(3, 'UTC') CODEC(Delta, ZSTD),
    read_bytes  Nullable(UInt64),
    write_bytes Nullable(UInt64)
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (org_id, node_id, ts, pid)
TTL toDateTime(ts) + INTERVAL 3 DAY;

CREATE TABLE IF NOT EXISTS {{.DB}}.events
(
    ts            DateTime64(3, 'UTC') CODEC(Delta, ZSTD),
    org_id        UUID,
    node_id       UUID,
    event_id      UUID,
    kind          Enum8('threshold_crossed' = 1, 'reachability_changed' = 2, 'sampler_version' = 3),
    metric        LowCardinality(String),
    tint          Enum8('ok' = 0, 'warn' = 1, 'critical' = 2),
    payload       String CODEC(ZSTD(3)),
    acked_by      Nullable(UUID),
    acked_at      Nullable(DateTime64(3, 'UTC')),
    snoozed_until Nullable(DateTime64(3, 'UTC'))
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (org_id, node_id, ts)
TTL toDateTime(ts) + INTERVAL 90 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS {{.DB}}.samples_1m
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
FROM {{.DB}}.samples_raw
GROUP BY ts_minute, org_id, node_id;

CREATE MATERIALIZED VIEW IF NOT EXISTS {{.DB}}.samples_5m
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
FROM {{.DB}}.samples_raw
GROUP BY ts_5m, org_id, node_id;
