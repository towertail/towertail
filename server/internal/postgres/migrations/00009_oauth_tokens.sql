-- +goose Up
ALTER TABLE api_tokens ADD COLUMN scopes TEXT[], ADD COLUMN audience TEXT, ADD COLUMN grant_id UUID;
CREATE INDEX api_tokens_grant_idx ON api_tokens (grant_id) WHERE grant_id IS NOT NULL;
CREATE INDEX api_tokens_oauth_exp_idx ON api_tokens (expires_at) WHERE kind = 'oauth';

-- +goose Down
DROP INDEX IF EXISTS api_tokens_oauth_exp_idx;
DROP INDEX IF EXISTS api_tokens_grant_idx;
ALTER TABLE api_tokens DROP COLUMN IF EXISTS grant_id, DROP COLUMN IF EXISTS audience, DROP COLUMN IF EXISTS scopes;
