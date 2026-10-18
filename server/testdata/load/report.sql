-- Load test report. Run after a load test:
--   clickhouse-client --database towertail --multiquery < testdata/load/report.sql
-- Each query uses the current database.

-- 1. Size and rows of each table. The inner table of an MV without TO
-- shows the MV name.
SELECT
    if(t.name != '', t.name || ' (inner)', p.table)    AS tbl,
    sum(rows)                                          AS row_count,
    formatReadableSize(sum(data_compressed_bytes))     AS compressed,
    formatReadableSize(sum(data_uncompressed_bytes))   AS uncompressed,
    round(sum(data_uncompressed_bytes) / greatest(sum(data_compressed_bytes), 1), 1) AS ratio,
    round(sum(data_compressed_bytes) / greatest(row_count, 1), 1) AS bytes_per_row
FROM system.parts AS p
LEFT JOIN (SELECT name, '.inner_id.' || toString(uuid) AS inner FROM system.tables WHERE database = currentDatabase()) AS t
    ON p.table = t.inner
WHERE p.active AND p.database = currentDatabase()
GROUP BY tbl
ORDER BY sum(data_compressed_bytes) DESC;

-- 2. Hosts and samples in samples_raw, and the bytes for each host for
-- each day of all the tables, from the time span of the data.
WITH
    (SELECT uniqExact(node_id) FROM samples_raw) AS hosts,
    (SELECT greatest(dateDiff('second', min(ts), max(ts)), 1) FROM samples_raw) AS span_s,
    (SELECT sum(data_compressed_bytes) FROM system.parts WHERE active AND database = currentDatabase()) AS bytes
SELECT
    hosts,
    (SELECT count() FROM samples_raw)                     AS samples,
    span_s,
    formatReadableSize(bytes)                             AS total_compressed,
    formatReadableSize(bytes / greatest(hosts, 1) * 86400 / span_s) AS bytes_per_host_per_day;

-- 3. Rows from the load in the detail tables.
SELECT 'procs_topn' AS table, uniqExact(node_id) AS hosts, count() AS row_count FROM procs_topn
UNION ALL SELECT 'disks', uniqExact(node_id), count() FROM disks
UNION ALL SELECT 'disk_io_devices', uniqExact(node_id), count() FROM disk_io_devices;

-- 4. Insert time for each table, from the query log (last hour). An
-- insert also writes the MVs of the table, and query_rows counts them.
SELECT
    arrayJoin(tables)                         AS table,
    count()                                   AS inserts,
    sum(written_rows)                         AS query_rows,
    round(avg(query_duration_ms), 1)          AS avg_ms,
    quantile(0.99)(query_duration_ms)         AS p99_ms
FROM system.query_log
WHERE type = 'QueryFinish' AND query_kind = 'Insert'
  AND event_time > now() - INTERVAL 1 HOUR
  AND has(databases, currentDatabase())
GROUP BY table
ORDER BY query_rows DESC;

-- 5. Server CPU and memory now.
SELECT metric, round(value, 2) AS value
FROM system.asynchronous_metrics
WHERE metric IN ('OSUserTimeNormalized', 'OSSystemTimeNormalized', 'OSIOWaitTimeNormalized',
                 'MemoryResident', 'OSMemoryAvailable', 'LoadAverage1')
ORDER BY metric;
