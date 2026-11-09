-- +goose NO TRANSACTION
-- +goose Up
-- A process that stays over the CPU limit of the long-process rule.
ALTER TABLE {{.DB}}.events MODIFY COLUMN kind Enum8('threshold_crossed' = 1, 'reachability_changed' = 2, 'sampler_version' = 3, 'long_process' = 4);
