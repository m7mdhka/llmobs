-- LLMObs ClickHouse schema — scale profile (ADR-0026, Arc L / L1).
--
-- Migration hygiene, learned from the Langfuse scale mine:
--   R-CH1  cluster name is NEVER a literal — the runner substitutes {{on_cluster}}
--          (never `ON CLUSTER default`); a CI grep-guard rejects the literal.
--   R-CH2  NFS/EFS-safe + idempotent: no CREATE OR REPLACE VIEW anywhere; every DDL
--          is IF NOT EXISTS so a re-run / partial multi-node apply is a no-op.
--   R-CH3  engine is templated so the SAME migration is deterministic under both
--          standalone / Replicated-database ({{engine_*}} → plain MergeTree family)
--          and Atomic + ON CLUSTER ({{engine_*}} → Replicated* with a {uuid} zk path).
--
-- Traces are DERIVED at query time from spans (DSL §4.1) — no traces table.
-- JSON maps/docs are stored as String (portable across CH versions; the canonical
-- doc is already a JSON string). is_deleted is a normal, read-filtered column (a
-- revivable field-group per merge semantics) — NOT the ReplacingMergeTree deleted
-- column, which would physically drop the tombstone row.

CREATE TABLE IF NOT EXISTS spans {{on_cluster}} (
    project_id             String,
    id                     String,
    trace_id               String,
    parent_span_id         String,
    kind                   LowCardinality(String),
    raw_kind               String,
    name                   String,
    start_time             DateTime64(6),
    end_time               Nullable(DateTime64(6)),
    completion_start_time  Nullable(DateTime64(6)),
    status_code            LowCardinality(String),
    environment            LowCardinality(String),
    release                String,
    version                String,
    session_id             String,
    user_id                String,
    model                  String,
    provider               LowCardinality(String),
    total_cost             Nullable(Float64),
    attributes             String,
    usage_details          String,
    cost_details           String,
    provided_usage_details String,
    provided_cost_details  String,
    prompt_ref             String,
    pricing_snapshot_ref   String,
    is_deleted             UInt8 DEFAULT 0,
    event_ts               DateTime64(6),
    ver                    UInt64,
    doc                    String,
    provenance             String DEFAULT '{}',
    updated_at             DateTime64(6) DEFAULT now64(6),
    INDEX idx_trace   trace_id   TYPE bloom_filter GRANULARITY 4,
    INDEX idx_kind    kind       TYPE set(0)       GRANULARITY 4,
    INDEX idx_session session_id TYPE bloom_filter GRANULARITY 4,
    INDEX idx_user    user_id    TYPE bloom_filter GRANULARITY 4
)
-- ReplacingMergeTree version = `ver` (a monotonic WRITE-order stamp), NOT event_ts.
-- Merge-on-write folds the incoming event into the current settled row, so the
-- LAST WRITE always incorporates every prior event (via the shared fold's per-field
-- provenance) — write-order is completeness-order. Versioning on event_ts would let
-- an out-of-order OLDER event's fully-folded row lose the RMT collapse to a stale
-- higher-event_ts row, silently dropping data. Same-key writes are serialized by the
-- ingest consumer partition, so `ver` is strictly increasing per (project_id, id).
ENGINE = {{engine_replacing_ver}}
PARTITION BY toYYYYMM(start_time)
-- trace_id ahead of kind clusters a trace's spans for the dominant tree fetch (LM-9);
-- (project_id, id) is the logical idempotency key — frozen start_time keeps this sort
-- key stable per (project_id, id), so id-reuse across a day boundary can't split a row
-- (the Langfuse date-boundary dedup hazard we're immune to, LM-6).
ORDER BY (project_id, toDate(start_time), trace_id, id);

CREATE TABLE IF NOT EXISTS scores {{on_cluster}} (
    project_id     String,
    id             String,
    subject_type   LowCardinality(String),
    subject_id     String,
    name           String,
    data_type      LowCardinality(String),
    value_numeric  Nullable(Float64),
    value_string   String,
    source         LowCardinality(String),
    timestamp      DateTime64(6),
    environment    LowCardinality(String),
    comment        String,
    metadata       String,
    config_ref     String,
    is_deleted     UInt8 DEFAULT 0,
    event_ts       DateTime64(6),
    ver            UInt64,
    doc            String,
    provenance     String DEFAULT '{}',
    updated_at     DateTime64(6) DEFAULT now64(6),
    INDEX idx_subject subject_id TYPE bloom_filter GRANULARITY 4
)
-- Version = write-order `ver`, not event_ts (see spans).
ENGINE = {{engine_replacing_ver}}
PARTITION BY toYYYYMM(timestamp)
ORDER BY (project_id, toDate(timestamp), subject_type, id);

-- Erasure-suppression tombstone (G3): a GDPR-erased (project_id, id) is refused on
-- re-delivery until expires_at, so replay can't resurrect it. Latest-wins by event_ts.
CREATE TABLE IF NOT EXISTS erasure_suppression {{on_cluster}} (
    project_id String,
    id         String,
    audit_id   String,
    erased_at  DateTime64(6) DEFAULT now64(6),
    expires_at DateTime64(6),
    event_ts   DateTime64(6) DEFAULT now64(6)
) ENGINE = {{engine_replacing_event_ts}}
ORDER BY (project_id, id);

-- Erasure audit proof row (append-only).
CREATE TABLE IF NOT EXISTS erasure_audit {{on_cluster}} (
    id         String,
    project_id String,
    actor      String,
    filter     String,
    row_count  UInt64,
    created_at DateTime64(6) DEFAULT now64(6)
) ENGINE = {{engine_mergetree}}
ORDER BY (project_id, created_at);
