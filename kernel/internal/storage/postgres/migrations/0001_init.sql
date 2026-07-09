-- Lite-profile schema: minimal control plane + the spans store.
-- Storage-neutral canonical span is kept in `doc` (JSONB, the fold output);
-- promoted columns are maintained alongside for indexed querying (99-adapter-guidance).

CREATE TABLE IF NOT EXISTS organizations (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS projects (
    id          TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- API keys: public key id + hashed secret. Scopes gate ingest vs query.
CREATE TABLE IF NOT EXISTS api_keys (
    public_key      TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    hashed_secret   TEXT NOT NULL,
    scopes          TEXT[] NOT NULL DEFAULT '{}',   -- e.g. {ingest,query}
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_api_keys_project ON api_keys(project_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_secret ON api_keys(hashed_secret);

CREATE TABLE IF NOT EXISTS spans (
    project_id              TEXT NOT NULL,
    id                      TEXT NOT NULL,
    trace_id                TEXT,
    parent_span_id          TEXT,
    kind                    TEXT,
    raw_kind                TEXT,
    name                    TEXT,
    start_time              TIMESTAMPTZ,
    end_time                TIMESTAMPTZ,
    status_code             TEXT,
    environment             TEXT,
    release                 TEXT,
    version                 TEXT,
    session_id              TEXT,
    user_id                 TEXT,
    model                   TEXT,
    provider                TEXT,
    total_cost              NUMERIC,
    attributes              JSONB NOT NULL DEFAULT '{}'::jsonb,
    usage_details           JSONB NOT NULL DEFAULT '{}'::jsonb,
    cost_details            JSONB NOT NULL DEFAULT '{}'::jsonb,
    provided_usage_details  JSONB NOT NULL DEFAULT '{}'::jsonb,
    provided_cost_details   JSONB NOT NULL DEFAULT '{}'::jsonb,
    prompt_ref              JSONB,
    pricing_snapshot_ref    JSONB,
    is_deleted              BOOLEAN NOT NULL DEFAULT false,
    event_ts                TIMESTAMPTZ NOT NULL,
    doc                     JSONB NOT NULL,          -- the full folded canonical span
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, id)
);

-- Promoted-set indexes (tree-first + dimensions); GIN for attribute-map queries.
CREATE INDEX IF NOT EXISTS idx_spans_time        ON spans (project_id, start_time DESC, id);
CREATE INDEX IF NOT EXISTS idx_spans_trace       ON spans (project_id, trace_id);
CREATE INDEX IF NOT EXISTS idx_spans_kind        ON spans (project_id, kind);
CREATE INDEX IF NOT EXISTS idx_spans_session     ON spans (project_id, session_id);
CREATE INDEX IF NOT EXISTS idx_spans_user        ON spans (project_id, user_id);
CREATE INDEX IF NOT EXISTS idx_spans_attrs_gin   ON spans USING GIN (attributes);
