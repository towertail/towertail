-- +goose Up
CREATE TABLE IF NOT EXISTS api_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id      UUID REFERENCES users(id) ON DELETE SET NULL,
    kind         TEXT NOT NULL,
    token_hash   BYTEA NOT NULL UNIQUE,
    label        TEXT,
    node_id      UUID,
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS api_tokens_org_id_idx ON api_tokens (org_id);

-- +goose Down
DROP TABLE IF EXISTS api_tokens;
