-- +goose NO TRANSACTION
-- +goose Up
-- One row for each AI tool call. The row keeps the arguments and the size of
-- the result, never the values of the result. Rows older than 30 days go.
CREATE TABLE IF NOT EXISTS {{.DB}}.ai_calls
(
    ts          DateTime64(3, 'UTC') CODEC(Delta, ZSTD),
    call_id     UUID,
    org_id      UUID,
    user_id     Nullable(UUID),
    token_id    UUID,
    grant_id    Nullable(UUID),
    via         Enum8('api_key' = 1, 'oauth' = 2, 'user' = 3, 'assistant' = 4),
    client      LowCardinality(String),
    tool        LowCardinality(String),
    input       String CODEC(ZSTD(3)),
    status      Enum8('ok' = 1, 'error' = 2, 'denied' = 3, 'rate_limited' = 4),
    clamped     Bool,
    error       String,
    duration_ms UInt32,
    rows        UInt32,
    bytes       UInt32,
    change      String CODEC(ZSTD(3))
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (org_id, ts, call_id)
TTL toDateTime(ts) + INTERVAL 30 DAY;
