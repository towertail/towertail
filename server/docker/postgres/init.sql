-- Seeded when Postgres first boots (cloud-managed profile only).
-- Applied before goose migrations run; only creates extension requirements.
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
