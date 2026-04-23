CREATE TABLE IF NOT EXISTS towertail.disk_io_devices
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
TTL toDateTime(ts) + INTERVAL 7 DAY
