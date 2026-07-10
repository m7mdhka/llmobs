-- Scores (PR-E2, closes #15). A score is a measurement attached to a subject
-- (LM-3/LM-8). Merge-on-write, same fold as spans/traces; idempotency key
-- (project_id, id). Promoted columns mirror fields.json 'scores'; `doc` is the
-- authoritative folded value, `provenance` carries per-field-group stamps.
CREATE TABLE IF NOT EXISTS scores (
    project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    id             TEXT NOT NULL,
    subject_type   TEXT NOT NULL,          -- kernel: span|trace|session; plugin: ns/name
    subject_id     TEXT NOT NULL,
    name           TEXT NOT NULL,
    data_type      TEXT NOT NULL,          -- numeric | categorical | boolean
    value_numeric  DOUBLE PRECISION,
    value_string   TEXT,
    source         TEXT,                   -- annotation | eval | heuristic | plugin (04-score.md §4)
    timestamp      TIMESTAMPTZ NOT NULL,
    environment    TEXT NOT NULL DEFAULT 'default',
    comment        TEXT,
    metadata       JSONB NOT NULL DEFAULT '{}'::jsonb,
    config_ref     JSONB,
    is_deleted     BOOLEAN NOT NULL DEFAULT false,
    event_ts       TIMESTAMPTZ NOT NULL,
    doc            JSONB NOT NULL,
    provenance     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, id)
);

-- Subject lookup is the hot path for the QD-9 traces semi-join and subject reads.
CREATE INDEX IF NOT EXISTS idx_scores_subject ON scores (project_id, subject_type, subject_id);
CREATE INDEX IF NOT EXISTS idx_scores_time    ON scores (project_id, timestamp DESC, id);
CREATE INDEX IF NOT EXISTS idx_scores_name    ON scores (project_id, name);
CREATE INDEX IF NOT EXISTS idx_scores_meta    ON scores USING GIN (metadata);
