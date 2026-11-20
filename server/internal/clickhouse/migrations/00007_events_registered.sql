-- +goose NO TRANSACTION
-- +goose Up
-- A node that enrolled. The alerter reads it to find nodes that never report.
ALTER TABLE {{.DB}}.events MODIFY COLUMN kind Enum8('threshold_crossed' = 1, 'reachability_changed' = 2, 'sampler_version' = 3, 'long_process' = 4, 'registered' = 5);
