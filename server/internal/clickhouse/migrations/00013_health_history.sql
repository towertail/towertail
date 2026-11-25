-- +goose NO TRANSACTION
-- +goose Up
-- Health checks in the history. The raw columns are null for a sampler or an
-- OS that does not report the value, and for rows from before this migration.
ALTER TABLE {{.DB}}.samples_raw
    ADD COLUMN IF NOT EXISTS zombies Nullable(UInt32),
    ADD COLUMN IF NOT EXISTS pids_used Nullable(UInt64),
    ADD COLUMN IF NOT EXISTS pids_max Nullable(UInt64),
    ADD COLUMN IF NOT EXISTS files_used Nullable(UInt64),
    ADD COLUMN IF NOT EXISTS files_max Nullable(UInt64),
    ADD COLUMN IF NOT EXISTS psi_cpu_some Nullable(Float32),
    ADD COLUMN IF NOT EXISTS psi_mem_some Nullable(Float32),
    ADD COLUMN IF NOT EXISTS psi_mem_full Nullable(Float32),
    ADD COLUMN IF NOT EXISTS psi_io_some Nullable(Float32),
    ADD COLUMN IF NOT EXISTS psi_io_full Nullable(Float32),
    ADD COLUMN IF NOT EXISTS mem_pressure_pct Nullable(UInt8),
    ADD COLUMN IF NOT EXISTS disk_ratio_max Nullable(Float32),
    ADD COLUMN IF NOT EXISTS inodes_ratio_max Nullable(Float32);

ALTER TABLE {{.DB}}.disks
    ADD COLUMN IF NOT EXISTS inodes_used Nullable(UInt64),
    ADD COLUMN IF NOT EXISTS inodes_total Nullable(UInt64);

-- Rollups of the health checks. Each state column keeps the average or the
-- maximum of one value. A value that no sample of the bucket has gives a null.
-- No backfill: the rollups start at the time of this migration.
CREATE TABLE IF NOT EXISTS {{.DB}}.health_1m
(
    ts_minute DateTime('UTC'),
    org_id UUID,
    node_id UUID,
    avg_load_1 AggregateFunction(avg, Nullable(Float64)),
    max_load_1 AggregateFunction(max, Nullable(Float64)),
    avg_procs AggregateFunction(avg, Nullable(Float64)),
    max_procs AggregateFunction(max, Nullable(Float64)),
    avg_disk AggregateFunction(avg, Nullable(Float64)),
    max_disk AggregateFunction(max, Nullable(Float64)),
    avg_inodes AggregateFunction(avg, Nullable(Float64)),
    max_inodes AggregateFunction(max, Nullable(Float64)),
    avg_zombies AggregateFunction(avg, Nullable(Float64)),
    max_zombies AggregateFunction(max, Nullable(Float64)),
    avg_pids AggregateFunction(avg, Nullable(Float64)),
    max_pids AggregateFunction(max, Nullable(Float64)),
    avg_files AggregateFunction(avg, Nullable(Float64)),
    max_files AggregateFunction(max, Nullable(Float64)),
    avg_psi_cpu AggregateFunction(avg, Nullable(Float64)),
    max_psi_cpu AggregateFunction(max, Nullable(Float64)),
    avg_psi_mem AggregateFunction(avg, Nullable(Float64)),
    max_psi_mem AggregateFunction(max, Nullable(Float64)),
    avg_psi_mem_full AggregateFunction(avg, Nullable(Float64)),
    max_psi_mem_full AggregateFunction(max, Nullable(Float64)),
    avg_psi_io AggregateFunction(avg, Nullable(Float64)),
    max_psi_io AggregateFunction(max, Nullable(Float64)),
    avg_psi_io_full AggregateFunction(avg, Nullable(Float64)),
    max_psi_io_full AggregateFunction(max, Nullable(Float64)),
    avg_mem_pressure AggregateFunction(avg, Nullable(Float64)),
    max_mem_pressure AggregateFunction(max, Nullable(Float64))
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMMDD(ts_minute)
ORDER BY (org_id, node_id, ts_minute)
TTL ts_minute + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS {{.DB}}.health_1m_mv TO {{.DB}}.health_1m
AS SELECT
    toStartOfMinute(ts) AS ts_minute,
    org_id,
    node_id,
    avgState(toNullable(toFloat64(cpu_load_1))) AS avg_load_1,
    maxState(toNullable(toFloat64(cpu_load_1))) AS max_load_1,
    avgState(toNullable(toFloat64(procs_total))) AS avg_procs,
    maxState(toNullable(toFloat64(procs_total))) AS max_procs,
    avgState(toNullable(toFloat64(disk_ratio_max))) AS avg_disk,
    maxState(toNullable(toFloat64(disk_ratio_max))) AS max_disk,
    avgState(toNullable(toFloat64(inodes_ratio_max))) AS avg_inodes,
    maxState(toNullable(toFloat64(inodes_ratio_max))) AS max_inodes,
    avgState(toNullable(toFloat64(zombies))) AS avg_zombies,
    maxState(toNullable(toFloat64(zombies))) AS max_zombies,
    avgState(toNullable(if(pids_max > 0, toFloat64(pids_used) / toFloat64(pids_max), NULL))) AS avg_pids,
    maxState(toNullable(if(pids_max > 0, toFloat64(pids_used) / toFloat64(pids_max), NULL))) AS max_pids,
    avgState(toNullable(if(files_max > 0, toFloat64(files_used) / toFloat64(files_max), NULL))) AS avg_files,
    maxState(toNullable(if(files_max > 0, toFloat64(files_used) / toFloat64(files_max), NULL))) AS max_files,
    avgState(toNullable(toFloat64(psi_cpu_some))) AS avg_psi_cpu,
    maxState(toNullable(toFloat64(psi_cpu_some))) AS max_psi_cpu,
    avgState(toNullable(toFloat64(psi_mem_some))) AS avg_psi_mem,
    maxState(toNullable(toFloat64(psi_mem_some))) AS max_psi_mem,
    avgState(toNullable(toFloat64(psi_mem_full))) AS avg_psi_mem_full,
    maxState(toNullable(toFloat64(psi_mem_full))) AS max_psi_mem_full,
    avgState(toNullable(toFloat64(psi_io_some))) AS avg_psi_io,
    maxState(toNullable(toFloat64(psi_io_some))) AS max_psi_io,
    avgState(toNullable(toFloat64(psi_io_full))) AS avg_psi_io_full,
    maxState(toNullable(toFloat64(psi_io_full))) AS max_psi_io_full,
    avgState(toNullable(toFloat64(mem_pressure_pct))) AS avg_mem_pressure,
    maxState(toNullable(toFloat64(mem_pressure_pct))) AS max_mem_pressure
FROM {{.DB}}.samples_raw
GROUP BY ts_minute, org_id, node_id;

CREATE TABLE IF NOT EXISTS {{.DB}}.health_5m
(
    ts_5m DateTime('UTC'),
    org_id UUID,
    node_id UUID,
    avg_load_1 AggregateFunction(avg, Nullable(Float64)),
    max_load_1 AggregateFunction(max, Nullable(Float64)),
    avg_procs AggregateFunction(avg, Nullable(Float64)),
    max_procs AggregateFunction(max, Nullable(Float64)),
    avg_disk AggregateFunction(avg, Nullable(Float64)),
    max_disk AggregateFunction(max, Nullable(Float64)),
    avg_inodes AggregateFunction(avg, Nullable(Float64)),
    max_inodes AggregateFunction(max, Nullable(Float64)),
    avg_zombies AggregateFunction(avg, Nullable(Float64)),
    max_zombies AggregateFunction(max, Nullable(Float64)),
    avg_pids AggregateFunction(avg, Nullable(Float64)),
    max_pids AggregateFunction(max, Nullable(Float64)),
    avg_files AggregateFunction(avg, Nullable(Float64)),
    max_files AggregateFunction(max, Nullable(Float64)),
    avg_psi_cpu AggregateFunction(avg, Nullable(Float64)),
    max_psi_cpu AggregateFunction(max, Nullable(Float64)),
    avg_psi_mem AggregateFunction(avg, Nullable(Float64)),
    max_psi_mem AggregateFunction(max, Nullable(Float64)),
    avg_psi_mem_full AggregateFunction(avg, Nullable(Float64)),
    max_psi_mem_full AggregateFunction(max, Nullable(Float64)),
    avg_psi_io AggregateFunction(avg, Nullable(Float64)),
    max_psi_io AggregateFunction(max, Nullable(Float64)),
    avg_psi_io_full AggregateFunction(avg, Nullable(Float64)),
    max_psi_io_full AggregateFunction(max, Nullable(Float64)),
    avg_mem_pressure AggregateFunction(avg, Nullable(Float64)),
    max_mem_pressure AggregateFunction(max, Nullable(Float64))
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMMDD(ts_5m)
ORDER BY (org_id, node_id, ts_5m)
TTL ts_5m + INTERVAL 90 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS {{.DB}}.health_5m_mv TO {{.DB}}.health_5m
AS SELECT
    toStartOfFiveMinute(ts) AS ts_5m,
    org_id,
    node_id,
    avgState(toNullable(toFloat64(cpu_load_1))) AS avg_load_1,
    maxState(toNullable(toFloat64(cpu_load_1))) AS max_load_1,
    avgState(toNullable(toFloat64(procs_total))) AS avg_procs,
    maxState(toNullable(toFloat64(procs_total))) AS max_procs,
    avgState(toNullable(toFloat64(disk_ratio_max))) AS avg_disk,
    maxState(toNullable(toFloat64(disk_ratio_max))) AS max_disk,
    avgState(toNullable(toFloat64(inodes_ratio_max))) AS avg_inodes,
    maxState(toNullable(toFloat64(inodes_ratio_max))) AS max_inodes,
    avgState(toNullable(toFloat64(zombies))) AS avg_zombies,
    maxState(toNullable(toFloat64(zombies))) AS max_zombies,
    avgState(toNullable(if(pids_max > 0, toFloat64(pids_used) / toFloat64(pids_max), NULL))) AS avg_pids,
    maxState(toNullable(if(pids_max > 0, toFloat64(pids_used) / toFloat64(pids_max), NULL))) AS max_pids,
    avgState(toNullable(if(files_max > 0, toFloat64(files_used) / toFloat64(files_max), NULL))) AS avg_files,
    maxState(toNullable(if(files_max > 0, toFloat64(files_used) / toFloat64(files_max), NULL))) AS max_files,
    avgState(toNullable(toFloat64(psi_cpu_some))) AS avg_psi_cpu,
    maxState(toNullable(toFloat64(psi_cpu_some))) AS max_psi_cpu,
    avgState(toNullable(toFloat64(psi_mem_some))) AS avg_psi_mem,
    maxState(toNullable(toFloat64(psi_mem_some))) AS max_psi_mem,
    avgState(toNullable(toFloat64(psi_mem_full))) AS avg_psi_mem_full,
    maxState(toNullable(toFloat64(psi_mem_full))) AS max_psi_mem_full,
    avgState(toNullable(toFloat64(psi_io_some))) AS avg_psi_io,
    maxState(toNullable(toFloat64(psi_io_some))) AS max_psi_io,
    avgState(toNullable(toFloat64(psi_io_full))) AS avg_psi_io_full,
    maxState(toNullable(toFloat64(psi_io_full))) AS max_psi_io_full,
    avgState(toNullable(toFloat64(mem_pressure_pct))) AS avg_mem_pressure,
    maxState(toNullable(toFloat64(mem_pressure_pct))) AS max_mem_pressure
FROM {{.DB}}.samples_raw
GROUP BY ts_5m, org_id, node_id;

CREATE TABLE IF NOT EXISTS {{.DB}}.health_1h
(
    ts_hour DateTime('UTC'),
    org_id UUID,
    node_id UUID,
    avg_load_1 AggregateFunction(avg, Nullable(Float64)),
    max_load_1 AggregateFunction(max, Nullable(Float64)),
    avg_procs AggregateFunction(avg, Nullable(Float64)),
    max_procs AggregateFunction(max, Nullable(Float64)),
    avg_disk AggregateFunction(avg, Nullable(Float64)),
    max_disk AggregateFunction(max, Nullable(Float64)),
    avg_inodes AggregateFunction(avg, Nullable(Float64)),
    max_inodes AggregateFunction(max, Nullable(Float64)),
    avg_zombies AggregateFunction(avg, Nullable(Float64)),
    max_zombies AggregateFunction(max, Nullable(Float64)),
    avg_pids AggregateFunction(avg, Nullable(Float64)),
    max_pids AggregateFunction(max, Nullable(Float64)),
    avg_files AggregateFunction(avg, Nullable(Float64)),
    max_files AggregateFunction(max, Nullable(Float64)),
    avg_psi_cpu AggregateFunction(avg, Nullable(Float64)),
    max_psi_cpu AggregateFunction(max, Nullable(Float64)),
    avg_psi_mem AggregateFunction(avg, Nullable(Float64)),
    max_psi_mem AggregateFunction(max, Nullable(Float64)),
    avg_psi_mem_full AggregateFunction(avg, Nullable(Float64)),
    max_psi_mem_full AggregateFunction(max, Nullable(Float64)),
    avg_psi_io AggregateFunction(avg, Nullable(Float64)),
    max_psi_io AggregateFunction(max, Nullable(Float64)),
    avg_psi_io_full AggregateFunction(avg, Nullable(Float64)),
    max_psi_io_full AggregateFunction(max, Nullable(Float64)),
    avg_mem_pressure AggregateFunction(avg, Nullable(Float64)),
    max_mem_pressure AggregateFunction(max, Nullable(Float64))
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(ts_hour)
ORDER BY (org_id, node_id, ts_hour)
TTL ts_hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS {{.DB}}.health_1h_mv TO {{.DB}}.health_1h
AS SELECT
    toStartOfHour(ts) AS ts_hour,
    org_id,
    node_id,
    avgState(toNullable(toFloat64(cpu_load_1))) AS avg_load_1,
    maxState(toNullable(toFloat64(cpu_load_1))) AS max_load_1,
    avgState(toNullable(toFloat64(procs_total))) AS avg_procs,
    maxState(toNullable(toFloat64(procs_total))) AS max_procs,
    avgState(toNullable(toFloat64(disk_ratio_max))) AS avg_disk,
    maxState(toNullable(toFloat64(disk_ratio_max))) AS max_disk,
    avgState(toNullable(toFloat64(inodes_ratio_max))) AS avg_inodes,
    maxState(toNullable(toFloat64(inodes_ratio_max))) AS max_inodes,
    avgState(toNullable(toFloat64(zombies))) AS avg_zombies,
    maxState(toNullable(toFloat64(zombies))) AS max_zombies,
    avgState(toNullable(if(pids_max > 0, toFloat64(pids_used) / toFloat64(pids_max), NULL))) AS avg_pids,
    maxState(toNullable(if(pids_max > 0, toFloat64(pids_used) / toFloat64(pids_max), NULL))) AS max_pids,
    avgState(toNullable(if(files_max > 0, toFloat64(files_used) / toFloat64(files_max), NULL))) AS avg_files,
    maxState(toNullable(if(files_max > 0, toFloat64(files_used) / toFloat64(files_max), NULL))) AS max_files,
    avgState(toNullable(toFloat64(psi_cpu_some))) AS avg_psi_cpu,
    maxState(toNullable(toFloat64(psi_cpu_some))) AS max_psi_cpu,
    avgState(toNullable(toFloat64(psi_mem_some))) AS avg_psi_mem,
    maxState(toNullable(toFloat64(psi_mem_some))) AS max_psi_mem,
    avgState(toNullable(toFloat64(psi_mem_full))) AS avg_psi_mem_full,
    maxState(toNullable(toFloat64(psi_mem_full))) AS max_psi_mem_full,
    avgState(toNullable(toFloat64(psi_io_some))) AS avg_psi_io,
    maxState(toNullable(toFloat64(psi_io_some))) AS max_psi_io,
    avgState(toNullable(toFloat64(psi_io_full))) AS avg_psi_io_full,
    maxState(toNullable(toFloat64(psi_io_full))) AS max_psi_io_full,
    avgState(toNullable(toFloat64(mem_pressure_pct))) AS avg_mem_pressure,
    maxState(toNullable(toFloat64(mem_pressure_pct))) AS max_mem_pressure
FROM {{.DB}}.samples_raw
GROUP BY ts_hour, org_id, node_id;
