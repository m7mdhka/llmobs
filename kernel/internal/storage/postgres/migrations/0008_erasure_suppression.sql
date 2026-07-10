-- Erasure-suppression tombstones (Arc G / G3). GDPR erasure hard-deletes matching
-- spans, but the idempotent merge would happily RE-CREATE a span if the same
-- (project_id, id) is re-delivered afterwards (client retry, Collector replay,
-- Kafka redelivery). A GDPR erasure that a redelivery undoes is not erasure.
--
-- A suppression row records an erased (project_id, id) that the persist path
-- consults: a re-delivered span matching an UNEXPIRED suppression is dropped at
-- ingest, not folded. Design choice — per-span-id tombstone with a TTL:
--   * per-id (not predicate): precise — only the erased spans are blocked, so a
--     different span in the same trace is never collateral; and the persist-path
--     check is an O(1) primary-key lookup, not a per-span predicate scan.
--   * TTL (not forever): a GDPR request has a bounded resolution window, so the
--     tombstone need only outlive plausible redelivery (Collector/Kafka replay is
--     hours, not months), never grow unbounded. expires_at = erasure time + a
--     retention window (LLMOBS_ERASURE_SUPPRESSION_TTL, default 720h/30d).
-- This is distinct from the in-model soft-delete tombstone (is_deleted): that is a
-- mergeable, revivable state; this is a harder, ingest-blocking gate. See
-- api/model/v1alpha1/05-update-semantics.md §4a.
CREATE TABLE IF NOT EXISTS erasure_suppression (
    project_id  TEXT NOT NULL,
    id          TEXT NOT NULL,          -- the erased span id
    audit_id    TEXT NOT NULL,          -- links to erasure_audit (provenance)
    erased_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,   -- after this, the tombstone may be reaped
    PRIMARY KEY (project_id, id)
);
-- Expiry index so a future reaper can prune cheaply (the persist-path check is by
-- PK and does not need it).
CREATE INDEX IF NOT EXISTS idx_erasure_suppression_expiry ON erasure_suppression (expires_at);
