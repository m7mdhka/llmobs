-- Per-plugin key/value store (Arc H / H4, the `kv` primitive). Kernel-owned,
-- plugin-namespaced, tenant-scoped: every row is keyed by (plugin_id, project_id,
-- key) so a plugin's kv is isolated per project and never collides with another
-- plugin. The plugin reaches this only through the SDK `kv` primitive over the
-- double-token path — it never gets a connection string (invariant 3).
CREATE TABLE IF NOT EXISTS plugin_kv (
    plugin_id  TEXT NOT NULL,
    project_id TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (plugin_id, project_id, key)
);
