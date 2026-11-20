-- +goose Up
ALTER TABLE api_tokens ADD COLUMN expires_at TIMESTAMPTZ;
ALTER TABLE nodes ADD COLUMN machine_id TEXT;
CREATE INDEX nodes_machine_id_idx ON nodes (org_id, machine_id) WHERE machine_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS nodes_machine_id_idx;
ALTER TABLE nodes DROP COLUMN IF EXISTS machine_id;
ALTER TABLE api_tokens DROP COLUMN IF EXISTS expires_at;
