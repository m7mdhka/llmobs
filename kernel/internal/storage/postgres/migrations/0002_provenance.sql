-- Per-field provenance (issue #17): a JSONB map of field-group path -> the
-- (event_ts, event_id) stamp of the event that currently owns that group. The
-- merge-on-write fold in store.go folds each incoming event against these
-- per-group stamps, so out-of-order updates converge to the same state as the
-- ordered Fold. Existing rows default to '{}' — their next update reseeds the
-- provenance from the stored doc.
ALTER TABLE spans
    ADD COLUMN IF NOT EXISTS provenance JSONB NOT NULL DEFAULT '{}'::jsonb;
