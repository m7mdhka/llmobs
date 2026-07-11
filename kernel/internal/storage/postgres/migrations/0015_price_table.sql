-- Price table (Arc M / M1, ADR-0029). The user-editable, versioned pricing the
-- cost-derivation enrich stage reads. Control-plane metadata (Postgres, BOTH profiles);
-- there is no ClickHouse price table.
--
-- APPEND-ONLY, versioned: an edit to a (provider, model) price INSERTs a new version
-- row; prior versions are NEVER updated or deleted, so a derived cost's
-- pricing_snapshot_ref (= this row's id) stays resolvable and a re-pricing backfill can
-- recompute against the exact historical rate (06-usage-cost.md §5). Provider/model are
-- stored CANONICAL (internal/pricing.Canonical*), applied identically at write + lookup
-- (R6). Rates/tiers are data-driven JSONB (R3/R4/R7).
CREATE TABLE IF NOT EXISTS price_entries (
    id             TEXT PRIMARY KEY,            -- "<provider>/<model>#<version>" (the pricing_snapshot_ref id)
    provider       TEXT NOT NULL,               -- canonical provider (openai, anthropic, google, azure, bedrock, …)
    model          TEXT NOT NULL,               -- canonical model key (prefix-stripped, lowercased)
    version        INTEGER NOT NULL,            -- monotonic per (provider, model)
    effective_from TIMESTAMPTZ NOT NULL,        -- applies to spans with start_time >= this (newest such version wins)
    rates          JSONB NOT NULL,              -- { "<key>": { "per_token": <num>, "reduces": "input"|"output"|absent }, … }
    tiers          JSONB NOT NULL DEFAULT '[]'::jsonb, -- [ { "key", "threshold_tokens", "per_token" }, … ] (above-threshold rates)
    source         TEXT NOT NULL DEFAULT 'default', -- 'default' (seeded) | 'override' (operator)
    raw_provider   TEXT,                        -- verbatim provider an operator supplied (preserved, never keyed on)
    created_by     TEXT,                        -- actor (session identity) for audit; null for seeds
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, model, version)
);

-- Lookup index: newest applicable version for a (provider, model) at a span's time.
CREATE INDEX IF NOT EXISTS idx_price_lookup
    ON price_entries (provider, model, effective_from DESC, version DESC);

-- Per-project discount factor (ADR-0029 D6). A multiplier a project negotiated,
-- applied to derived cost at derivation (M2). This is the ONLY tenant-scoped pricing
-- surface: a project may set its own discount, never a global list price. factor in
-- (0,1] is a discount (0.8 = 20% off); a mutable single row per project.
CREATE TABLE IF NOT EXISTS price_discounts (
    project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    factor     DOUBLE PRECISION NOT NULL,
    updated_by TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
