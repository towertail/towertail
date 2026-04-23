CREATE DATABASE IF NOT EXISTS towertail;

CREATE TABLE IF NOT EXISTS towertail.samples_raw
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
SETTINGS index_granularity = 8192
