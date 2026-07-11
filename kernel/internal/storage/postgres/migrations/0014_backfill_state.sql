-- Lite→scale backfill state (Arc L / L5, RULING-MIG6). The dual-read layer makes
-- migration OPTIONAL — historical lite data is always readable through the unified
-- view — so this backfill is a CONVENIENCE that copies settled lite rows into the
-- scale adapter in the background. It is resumable: the cursor is a (ts, project_id,
-- id) TOTAL order, persisted after every batch, so a restart continues exactly where
-- it left off and same-timestamp rows never loop (the #7117 trap). Decoupled from
-- boot readiness — a partially-migrated store is fully correct via dual-read.
CREATE TABLE IF NOT EXISTS backfill_state (
    kind        TEXT PRIMARY KEY,       -- 'spans' | 'scores'
    cursor_ts   TIMESTAMPTZ,            -- resume anchor: last migrated row's ts (NULL => not started)
    cursor_proj TEXT NOT NULL DEFAULT '', -- resume anchor: project_id (total-order tiebreak)
    cursor_id   TEXT NOT NULL DEFAULT '', -- resume anchor: entity id (total-order tiebreak)
    done        BOOLEAN NOT NULL DEFAULT false, -- the kind is fully migrated
    migrated    BIGINT NOT NULL DEFAULT 0,       -- rows copied so far (visible progress)
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Dead-letter for records that PERMANENTLY fail to migrate (malformed settled doc,
-- a deterministic scale rejection). Per the failure taxonomy (CLAUDE.md #12) a
-- permanent failure is recorded here and skipped so the backfill makes progress —
-- NEVER silently dropped, NEVER retried forever pinning the run. Transient failures
-- (scale down, a blip) are retried with backoff and are not recorded here.
CREATE TABLE IF NOT EXISTS backfill_deadletter (
    kind        TEXT NOT NULL,          -- 'spans' | 'scores'
    project_id  TEXT NOT NULL,
    id          TEXT NOT NULL,
    reason      TEXT NOT NULL,
    at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, project_id, id)
);
