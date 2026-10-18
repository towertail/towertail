-- +goose NO TRANSACTION
-- +goose Up
-- The top 10 processes of each host for each minute. A process is a
-- name and a cmd hash. A plain MV sees one insert block and cannot rank
-- across blocks, so a refreshable MV ranks the last 2 full minutes each
-- minute. Each minute thus comes 2 times. The key has the rank, so the
-- later pass replaces the earlier one (read with FINAL).
CREATE TABLE IF NOT EXISTS {{.DB}}.procs_top_1m
(
    ts_minute    DateTime('UTC'),
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
PARTITION BY toYYYYMMDD(ts_minute)
ORDER BY (org_id, node_id, ts_minute, rank)
TTL ts_minute + INTERVAL 7 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS {{.DB}}.procs_top_1m_mv
REFRESH EVERY 1 MINUTE APPEND TO {{.DB}}.procs_top_1m
AS SELECT
    ts_minute,
    org_id,
    node_id,
    toUInt8(row_number() OVER (PARTITION BY org_id, node_id, ts_minute ORDER BY cpu_pct_avg DESC, rss_max DESC)) AS rank,
    name,
    cmd_hash,
    any_cmd AS cmd,
    cpu_pct_avg,
    cpu_pct_max,
    rss_max,
    now() AS refreshed_at
FROM
(
    SELECT
        toStartOfMinute(ts)        AS ts_minute,
        org_id,
        node_id,
        name,
        cityHash64(cmd)            AS cmd_hash,
        any(cmd)                   AS any_cmd,
        toFloat32(avg(cpu_pct))    AS cpu_pct_avg,
        max(cpu_pct)               AS cpu_pct_max,
        max(rss)                   AS rss_max
    FROM {{.DB}}.procs_topn
    WHERE ts >= toStartOfMinute(now()) - INTERVAL 2 MINUTE
      AND ts <  toStartOfMinute(now())
    GROUP BY ts_minute, org_id, node_id, name, cmd_hash
)
ORDER BY org_id, node_id, ts_minute, cpu_pct_avg DESC
LIMIT 10 BY org_id, node_id, ts_minute;
