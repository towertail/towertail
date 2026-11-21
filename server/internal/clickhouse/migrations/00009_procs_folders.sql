-- +goose NO TRANSACTION
-- +goose Up
-- Working folder, repo, parent name and disk I/O rate of each process.
-- The rollups group by repo, so the views are made again with the repo.
ALTER TABLE {{.DB}}.procs_topn
    ADD COLUMN IF NOT EXISTS cwd String CODEC(ZSTD(3)),
    ADD COLUMN IF NOT EXISTS repo String CODEC(ZSTD(3)),
    ADD COLUMN IF NOT EXISTS parent LowCardinality(String),
    ADD COLUMN IF NOT EXISTS read_bps Nullable(UInt64),
    ADD COLUMN IF NOT EXISTS write_bps Nullable(UInt64);

ALTER TABLE {{.DB}}.procs_top_1m ADD COLUMN IF NOT EXISTS repo String CODEC(ZSTD(3));
ALTER TABLE {{.DB}}.procs_top_1h ADD COLUMN IF NOT EXISTS repo String CODEC(ZSTD(3));

DROP VIEW IF EXISTS {{.DB}}.procs_top_1m_mv;
DROP VIEW IF EXISTS {{.DB}}.procs_top_1h_mv;

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
    now() AS refreshed_at,
    repo
FROM
(
    SELECT
        toStartOfMinute(ts)        AS ts_minute,
        org_id,
        node_id,
        name,
        cityHash64(cmd)            AS cmd_hash,
        repo,
        any(cmd)                   AS any_cmd,
        toFloat32(avg(cpu_pct))    AS cpu_pct_avg,
        max(cpu_pct)               AS cpu_pct_max,
        max(rss)                   AS rss_max
    FROM {{.DB}}.procs_topn
    WHERE ts >= toStartOfMinute(now()) - INTERVAL 2 MINUTE
      AND ts <  toStartOfMinute(now())
    GROUP BY ts_minute, org_id, node_id, name, cmd_hash, repo
)
ORDER BY org_id, node_id, ts_minute, cpu_pct_avg DESC
LIMIT 10 BY org_id, node_id, ts_minute;

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
    now() AS refreshed_at,
    repo
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
        repo,
        any(cmd)                              AS any_cmd,
        toFloat32(sum(cpu_pct_avg) / least(60, greatest(1, dateDiff('minute', ts_hour, toStartOfMinute(now()))))) AS cpu_pct_avg,
        max(cpu_pct_max)                      AS cpu_pct_max,
        max(rss_max)                          AS rss_max
    FROM {{.DB}}.procs_top_1m FINAL
    WHERE ts_minute >= toStartOfHour(now()) - INTERVAL 1 HOUR
    GROUP BY ts_hour, org_id, node_id, name, cmd_hash, repo
)
ORDER BY org_id, node_id, ts_hour, cpu_pct_avg DESC
LIMIT 5 BY org_id, node_id, ts_hour;
