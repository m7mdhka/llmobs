-- Erasure audit (PR-E3, audit item 16 / Ingrid). Every GDPR erasure records who
-- ran it, the exact filter, the row count, and when — so deletion is provable.
CREATE TABLE IF NOT EXISTS erasure_audit (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL,
    actor       TEXT NOT NULL,          -- the identity that requested erasure
    filter      JSONB NOT NULL,         -- {user_id, from, to}
    row_count   INTEGER NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_erasure_project ON erasure_audit (project_id, created_at DESC);
