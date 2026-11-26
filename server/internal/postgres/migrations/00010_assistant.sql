-- +goose Up
CREATE TABLE IF NOT EXISTS assistant_chats (
    id         UUID PRIMARY KEY,
    org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL,
    title      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS assistant_chats_user_idx ON assistant_chats (org_id, user_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS assistant_chats_updated_idx ON assistant_chats (org_id, updated_at);

CREATE TABLE IF NOT EXISTS assistant_messages (
    id         UUID PRIMARY KEY,
    chat_id    UUID NOT NULL REFERENCES assistant_chats(id) ON DELETE CASCADE,
    seq        INT NOT NULL,
    role       TEXT NOT NULL,
    native     JSONB,
    parts      JSONB,
    model      TEXT NOT NULL DEFAULT '',
    usage      JSONB,
    status     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (chat_id, seq)
);

CREATE TABLE IF NOT EXISTS assistant_proposals (
    id         UUID PRIMARY KEY,
    org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    chat_id    UUID NOT NULL REFERENCES assistant_chats(id) ON DELETE CASCADE,
    message_id UUID,
    tool       TEXT NOT NULL,
    change     JSONB NOT NULL,
    status     TEXT NOT NULL,
    applied    JSONB,
    applied_by UUID,
    applied_at TIMESTAMPTZ,
    undone_at  TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS assistant_proposals_org_idx ON assistant_proposals (org_id, created_at DESC);
CREATE INDEX IF NOT EXISTS assistant_proposals_chat_idx ON assistant_proposals (chat_id);

CREATE TABLE IF NOT EXISTS assistant_usage (
    org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    scope      TEXT NOT NULL,
    subject    TEXT NOT NULL,
    period     TEXT NOT NULL,
    questions  INT NOT NULL DEFAULT 0,
    tokens     BIGINT NOT NULL DEFAULT 0,
    tool_calls INT NOT NULL DEFAULT 0,
    PRIMARY KEY (org_id, scope, subject, period)
);
-- +goose Down
DROP TABLE IF EXISTS assistant_usage;
DROP TABLE IF EXISTS assistant_proposals;
DROP TABLE IF EXISTS assistant_messages;
DROP TABLE IF EXISTS assistant_chats;
