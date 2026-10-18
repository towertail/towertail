-- +goose NO TRANSACTION
-- +goose Up
-- The top 5 processes of each host for each hour. The source is
-- procs_top_1m, because procs_topn keeps only 1 hour. Each 10 minutes a
-- refreshable MV ranks the current hour and the hour before it. The key
-- has the rank, so the later pass replaces the earlier one (read with
-- FINAL).
CREATE TABLE IF NOT EXISTS {{.DB}}.procs_top_1h
(
    ts_hour      DateTime('UTC'),
    org_id       UUID,
    node_id      UUID,
    rank         UInt8,
    name         LowCardinality(String),
    cmd_hash     UInt64,
    cmd          String CODEC(ZSTD(3)),
    cpu_pct_avg  Float32,
    cpu_pct_max  Float32,
    rss_max      UInt64,
    refreshed_at DateTime('UTC')
)
ENGINE = ReplacingMergeTree(refreshed_at)
PARTITION BY toYYYYMM(ts_hour)
ORDER BY (org_id, node_id, ts_hour, rank)
TTL ts_hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS {{.DB}}.procs_top_1h_mv
REFRESH EVERY 10 MINUTE APPEND TO {{.DB}}.procs_top_1h
AS SELECT
    ts_hour,
    org_id,
    node_id,
    toUInt8(row_number() OVER (PARTITION BY org_id, node_id, ts_hour ORDER BY cpu_pct_avg DESC, rss_max DESC)) AS rank,
    name,
    cmd_hash,
    any_cmd AS cmd,
    cpu_pct_avg,
    cpu_pct_max,
    rss_max,
    now() AS refreshed_at
FROM
(
    -- A minute where the process is not in the top 10 counts as 0. The
    -- current hour divides by the minutes that are done.
    SELECT
        toStartOfHour(ts_minute)              AS ts_hour,
        org_id,
        node_id,
        name,
        cmd_hash,
        any(cmd)                              AS any_cmd,
        toFloat32(sum(cpu_pct_avg) / least(60, greatest(1, dateDiff('minute', ts_hour, toStartOfMinute(now()))))) AS cpu_pct_avg,
        max(cpu_pct_max)                      AS cpu_pct_max,
        max(rss_max)                          AS rss_max
    FROM {{.DB}}.procs_top_1m FINAL
    WHERE ts_minute >= toStartOfHour(now()) - INTERVAL 1 HOUR
    GROUP BY ts_hour, org_id, node_id, name, cmd_hash
)
ORDER BY org_id, node_id, ts_hour, cpu_pct_avg DESC
LIMIT 5 BY org_id, node_id, ts_hour;
