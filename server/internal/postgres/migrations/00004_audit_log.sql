-- +goose Up
CREATE TABLE IF NOT EXISTS audit_log (
    id         BIGSERIAL PRIMARY KEY,
    org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    actor_id   UUID,
    action     TEXT NOT NULL,
    target     TEXT,
    payload    JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS audit_log_org_time_idx ON audit_log (org_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_log;
