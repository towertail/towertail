-- +goose NO TRANSACTION
-- +goose Up
-- Listening ports of each process. One row for each process with a
-- listener, for each ports snapshot of the sampler (ts is the time of the
-- snapshot). The rows of one snapshot merge into one.
CREATE TABLE IF NOT EXISTS {{.DB}}.proc_ports
(
    ts         DateTime64(3, 'UTC') CODEC(Delta, ZSTD),
    org_id     UUID,
    node_id    UUID,
    pid        Int32,
    name       String CODEC(ZSTD(3)),
    user_name  LowCardinality(String),
    listen_tcp Array(UInt16),
    listen_udp Array(UInt16),
    est_in     UInt32,
    est_out    UInt32
)
ENGINE = ReplacingMergeTree
ORDER BY (org_id, node_id, pid, ts)
TTL toDateTime(ts) + INTERVAL 1 DAY;
