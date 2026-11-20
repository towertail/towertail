-- +goose NO TRANSACTION
-- +goose Up
-- Alerts keep 365 days, the longest plan history.
ALTER TABLE {{.DB}}.events MODIFY TTL toDateTime(ts) + INTERVAL 365 DAY;
