CREATE TABLE IF NOT EXISTS towertail.procs_topn
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
TTL toDateTime(ts) + INTERVAL 3 DAY
