CREATE TABLE IF NOT EXISTS towertail.events
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
TTL toDateTime(ts) + INTERVAL 90 DAY
