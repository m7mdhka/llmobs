-- Re-pricing backfill state (Arc M / M4, 06-usage-cost.md §5). Because the price table
-- is APPEND-ONLY and every derived cost records the exact price version it used
-- (pricing_snapshot_ref.id), re-pricing is DETERMINISTIC, not best-effort: a job scans
-- the spans priced against a superseded version (or, for a discount change, a project's
-- derived spans), re-derives cost through the SAME §7.7 path, and re-emits an upsert of
-- only the cost field-groups. It reuses the L5 backfill discipline: a (ts, project_id,
-- id) TOTAL-ordered cursor persisted after every batch (same-timestamp rows never loop,
-- #7117); a SEPARATE generous execution budget (not the interactive read timeout that
-- broke v4's own backfill); resumable and decoupled from boot readiness.
--
-- run_key identifies one re-pricing run so concurrent/sequential runs resume
-- independently: the superseded snapshot-ref id for a price change ("openai/gpt-4o#1"),
-- or "discount:<project_id>" for a per-project discount change.
CREATE TABLE IF NOT EXISTS reprice_state (
    run_key     TEXT PRIMARY KEY,
    cursor_ts   TIMESTAMPTZ,                     -- resume anchor: last scanned row's start_time (NULL => not started)
    cursor_proj TEXT NOT NULL DEFAULT '',        -- resume anchor: project_id (total-order tiebreak)
    cursor_id   TEXT NOT NULL DEFAULT '',        -- resume anchor: span id (total-order tiebreak)
    done        BOOLEAN NOT NULL DEFAULT false,  -- the run scanned every matching span
    repriced    BIGINT NOT NULL DEFAULT 0,       -- spans whose cost ACTUALLY changed (re-emitted)
    scanned     BIGINT NOT NULL DEFAULT 0,       -- spans examined (repriced + unchanged no-ops)
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Dead-letter for spans that PERMANENTLY fail to re-price (a data-integrity anomaly:
-- a derived span whose own (provider, model) no longer resolves to any price entry —
-- structurally impossible under append-only pricing, so recorded and audited rather
-- than silently nulled). Per the failure taxonomy (CLAUDE.md #12) a permanent failure
-- is recorded here and stepped over so the run makes progress — NEVER silently dropped,
-- NEVER retried forever. A TRANSIENT failure (price-store or persist blip) is NOT
-- recorded here: it stops the run loud and resumable, so a blip never rewrites an
-- existing cost to null.
CREATE TABLE IF NOT EXISTS reprice_deadletter (
    run_key    TEXT NOT NULL,
    project_id TEXT NOT NULL,
    id         TEXT NOT NULL,
    reason     TEXT NOT NULL,
    at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_key, project_id, id)
);
