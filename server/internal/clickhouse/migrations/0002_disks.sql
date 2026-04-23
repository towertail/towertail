CREATE TABLE IF NOT EXISTS towertail.disks
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
TTL toDateTime(ts) + INTERVAL 7 DAY
