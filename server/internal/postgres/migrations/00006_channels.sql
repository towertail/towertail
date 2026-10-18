-- +goose Up
CREATE TABLE IF NOT EXISTS channels (
    org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    id         UUID NOT NULL,
    data       JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, id)
);

-- +goose Down
DROP TABLE IF EXISTS channels;
