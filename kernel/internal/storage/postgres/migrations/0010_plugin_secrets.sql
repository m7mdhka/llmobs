-- Per-plugin encrypted secret store (Arc H / H4, the `secrets` primitive). Only
-- ciphertext is ever stored; the plaintext is never persisted, never logged, and
-- never returned by any read except the owning plugin's authenticated delivery.
-- Envelope-encrypted with the kernel master key (in-memory for lite; KMS seam
-- deferred). Tenant- + plugin-scoped by the primary key.
CREATE TABLE IF NOT EXISTS plugin_secrets (
    plugin_id  TEXT NOT NULL,
    project_id TEXT NOT NULL,
    name       TEXT NOT NULL,
    ciphertext BYTEA NOT NULL,   -- AES-256-GCM ciphertext (+ tag); never plaintext
    nonce      BYTEA NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (plugin_id, project_id, name)
);
