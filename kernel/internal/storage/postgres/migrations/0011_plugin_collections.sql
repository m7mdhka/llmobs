-- Plugin collection registry (Arc H / H5, the `store` primitive). Each plugin's
-- declared collections live in a per-plugin Postgres SCHEMA (R1: plugin-namespaced
-- schemas in the shared DB), created/migrated by the kernel during the plugin's
-- `starting` phase. This registry records each provisioned collection's spec so
-- any replica can validate queries and extract indexed values without re-reading
-- the manifest. The per-plugin schemas + collection tables are created by DDL at
-- provision time (idempotent), not by migration files.
CREATE TABLE IF NOT EXISTS plugin_collections (
    plugin_id   TEXT NOT NULL,
    collection  TEXT NOT NULL,
    spec        JSONB NOT NULL,           -- CollectionSpec (fields + indexed flags)
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (plugin_id, collection)
);
