-- +goose Up
-- The org of the self-hosted mode and of the bootstrap token.
INSERT INTO orgs (id, name) VALUES ('00000000-0000-0000-0000-000000000000', 'default')
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS nodes (
    org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    id         UUID NOT NULL,
    data       JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, id)
);

CREATE TABLE IF NOT EXISTS settings (
    org_id     UUID PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
    data       JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS control_messages (
    id         UUID PRIMARY KEY,
    node_id    UUID NOT NULL,
    kind       TEXT NOT NULL,
    payload    BYTEA,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS control_messages_node_idx ON control_messages (node_id, created_at, id);

-- +goose Down
DROP TABLE IF EXISTS control_messages;
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS nodes;
DELETE FROM orgs WHERE id = '00000000-0000-0000-0000-000000000000';
