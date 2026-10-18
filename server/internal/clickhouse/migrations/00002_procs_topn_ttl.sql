-- +goose NO TRANSACTION
-- +goose Up
-- procs_top_1m and procs_top_1h keep the longer history.
ALTER TABLE {{.DB}}.procs_topn MODIFY TTL toDateTime(ts) + INTERVAL 1 HOUR;
