-- API keys move to argon2id (issue #9). The argon2id hash carries a per-key
-- random salt, so it can no longer serve as the lookup index. A deterministic
-- SHA-256 selector (lookup_hash) indexes the row; hashed_secret now holds the
-- argon2id verifier. The old uniqueness on hashed_secret is meaningless once
-- salted, so it is dropped in favor of a unique lookup_hash.
ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS lookup_hash TEXT;

DROP INDEX IF EXISTS idx_api_keys_secret;
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_lookup ON api_keys(lookup_hash);
