-- +goose NO TRANSACTION
-- +goose Up
-- One id for each alert (incident): the event id of the change out of ok.
-- Old rows read alert_id = event_id.
ALTER TABLE {{.DB}}.events ADD COLUMN IF NOT EXISTS alert_id UUID DEFAULT event_id AFTER event_id;

-- Append-only lines of acks, snoozes and closes. They replace the mutations
-- of acked_by, acked_at and snoozed_until on events.
CREATE TABLE IF NOT EXISTS {{.DB}}.alert_log
(
    ts       DateTime64(3, 'UTC') CODEC(Delta, ZSTD),
    org_id   UUID,
    node_id  UUID,
    alert_id UUID,
    kind     Enum8('notified' = 1, 'escalated' = 2, 'acked' = 3, 'unacked' = 4, 'snoozed' = 5, 'unsnoozed' = 6, 'closed' = 7),
    user     Nullable(UUID),
    until    Nullable(DateTime64(3, 'UTC')),
    text     String CODEC(ZSTD(3))
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (org_id, alert_id, ts)
TTL toDateTime(ts) + INTERVAL 365 DAY;
