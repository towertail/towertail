-- +goose Up
CREATE TABLE IF NOT EXISTS rules_backups (
    id         UUID PRIMARY KEY,
    org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    data       JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS rules_backups_org_idx ON rules_backups (org_id, created_at DESC);
-- +goose Down
DROP TABLE IF EXISTS rules_backups;
