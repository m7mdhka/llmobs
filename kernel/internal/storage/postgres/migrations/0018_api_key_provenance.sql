-- Arc O / O3 — API-key creator provenance.
-- Records which user (session) minted each machine API key, for audit + revocation
-- (O4). ON DELETE SET NULL: deleting the creator NEVER cascades to delete their keys
-- (that would silently break ingestion for a still-valid tenant) and NEVER orphans the
-- row — the provenance simply becomes NULL ("created by a since-deleted user").
ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS created_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL;
