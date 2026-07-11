# ADR-0029: The user-editable, versioned price table (cost-derivation foundation)

- **Status:** Accepted (Arc M / M1 — the cost-derivation foundation)
- **Date:** 2026-07-11
- **Deciders:** m7mdhka (Arc M)
- **Relates to:** the canonical usage/cost model (`api/model/v1alpha1/06-usage-cost.md`
  §4–§5, §7 R1–R8), ADR-0026 (control-plane stays Postgres in both profiles),
  ADR-0013/H6 (`jobs` primitive — the re-pricing backfill target). Evidence: the
  Langfuse + Opik cost mines (`docs/research/issue-13-cost-derivation-design-notes.md`)
  — the price-table treadmill was Langfuse's #1 self-host pain, and both incumbents
  shipped cost bugs from enumerating special cases instead of driving from price data.

## Context

The enrich stage (`pipeline/stages.go`) is a no-op today, so `total_cost` is null on
every derived trace — a real capability gap. Deriving cost needs prices, and the
price table is the artifact that has to exist first. The mines rule its shape:

- **It must be user-editable** without a code change. Langfuse's most-requested,
  oft-declined feature was "let me correct a price / add a model." A hardcoded map is
  the treadmill.
- **It must be versioned and history-preserving.** §5 mandates `pricing_snapshot_ref`
  so a derived cost is re-derivable and a price correction backfills history
  *deterministically* (§7.5/R7 + R8). That requires the exact price a span was billed
  against to remain retrievable after a later edit — i.e. edits cannot overwrite.
- **Pricing is data, not code** (§7.4/R4): one code path prices all providers and all
  detail buckets by reading rates off the entry; adding a provider or a new detail key
  is adding a row, never a branch.

## Decision

### D1 — The price table is control-plane metadata in Postgres, in BOTH profiles

Prices are reference metadata (small, read-heavy at ingest, edited rarely), not
telemetry. Per ADR-0026 the control plane is always Postgres — even in the scale
profile, where telemetry lives in ClickHouse. So the price table is a single Postgres
resource in both profiles; there is no ClickHouse price table. The enrich stage
(which runs in the pipeline that already holds the pool) reads it and caches it
in-process, invalidating on version change. This avoids putting mutable reference data
in append-only ClickHouse and keeps one source of truth.

### D2 — Append-only, versioned entries (the new schema shape this ADR establishes)

`price_entries` is **insert-only**: an edit to a `(provider, model)` price creates a
NEW version row; prior versions are never updated or deleted. This is the codebase's
first temporal/versioned table; it mirrors the `plugin_event_log` immutable-append
idiom (BIGSERIAL-style monotonic history) rather than the mutable-upsert idiom used
elsewhere (`plugin_kv`), because history preservation is the whole point.

- Natural key `(provider, model, version)`; `version` is monotonic per `(provider,
  model)`. Primary key `id = "<provider>/<model>#<version>"` — a stable, human-readable
  string that IS the snapshot reference (D4).
- `effective_from` selects which version applies to a span: derivation resolves the
  newest version with `effective_from <= span.start_time`. This gives time-correct
  pricing (a price change dated in the future does not retroactively re-bill history)
  and, with immutable versions, exact re-derivability.
- Provider/model are stored **canonical** (D3); the raw provider string is preserved
  on the span verbatim (§0/D6), never mutated.

### D3 — One canonical normalizer + provider-canonicalization table, applied at BOTH load and lookup (R6)

Model keys and provider identities normalize through ONE function
(`internal/pricing.Canonical*`), applied byte-identically when an entry is seeded/
written AND when it is looked up at derivation time. Asymmetric normalization = every
lookup misses = silent zero cost (Opik #5621). Provider aliases (`vertex_ai`/`gemini`/
`google_ai` → `google`; `azure` → its base; a provider-prefixed model
`openai/gpt-4o` → `gpt-4o`) resolve through the single table. A conformance fixture
proves symmetry in both directions.

### D4 — The snapshot reference is `(type=price, id="<provider>/<model>#<version>", label)`

A derived cost stores `pricing_snapshot_ref` as a `07-references.md` reference whose
`id` is the exact entry row's primary key (which encodes provider, model, AND version)
and whose `label` is a human snapshot (`"<provider>/<model> v<version>"`). Because a
version row is immutable and carries its whole rate schedule (base rates, detail
rates, AND tier schedule), a single `(id)` fully identifies everything a re-pricing
backfill needs — the tier schedule version (§7.5) is the entry version, not a separate
concept. Re-pricing (§5, R8, the H6 `jobs` primitive) finds spans whose
`pricing_snapshot_ref.id` points at a superseded entry and re-emits upsert events.

### D5 — Rates are a data-driven map with a declared residual base (R3/R4)

`rates` is a JSONB map `key -> { per_token, reduces? }`. `per_token` is the unit rate;
the optional `reduces` (`"input"` | `"output"`) declares which base count this bucket's
tokens are subtracted from, so the residual (`input − Σ(buckets reducing input)`, R3)
is computed by iterating the entry's rates — never a hardcoded "cache_read subtracts
from input" branch. A new detail key with a new rate and a `reduces` value just works.
`tiers` is a JSONB array of `{ key, threshold_tokens, per_token }` above-threshold
rates (R7). The derivation arithmetic itself lands in M2; M1 establishes the shape and
the store so the enrich stage is pure data-in.

### D6 — Global entries (admin-gated), per-project discount (project-gated)

`price_entries` are **instance-global** (no `project_id`) — a model's list price is the
same across a single instance's tenants, and editing it is an instance-admin action
(`perm.HasWriteAuthority`, the "#21 RBAC seam" until finer roles land). A per-project
**discount factor** (`price_discounts`, a multiplier a project negotiated) is the
tenant-scoped override; it is written under the project's own write authority and
applied at derivation (M2). Keeping global list prices separate from per-project
discounts is the tenant-isolation boundary: a project admin can never edit another
tenant's — or the global — list price, only their own discount.

### D7 — Money precision boundary (documented, per L2)

Rates and costs are `float64` in Go and JSON (matching the generated `cost_details
map[string]float64`) and `Decimal64(12)` in the ClickHouse adapter. Per-token rates are
small; the documented bound is that derived `total_cost` is exact to the model's
`Decimal64(12)` on the scale adapter and within float64 epsilon on the lite adapter —
the same cross-adapter precision bound L2 documents for aggregates.

## Consequences

- New `internal/pricing` package (the canonical normalizer + provider table, R6), a
  `postgres.PriceStore` (append-versioned CRUD + `Resolve` + seed + discount), a
  control-plane pricing API (list/get/upsert entries; get/set discount), migration
  `0015_price_table.sql`, and a default seed set. The price-entry shape is pinned in
  `api/schemas/pricing/v1alpha1/`.
- The enrich stage (M2) becomes pure data-in: resolve entry by canonical (provider,
  model, time), apply rates by the residual/tier rules, apply the project discount,
  stamp `pricing_snapshot_ref`. Trace-level no-double-count (R5) is M3; re-pricing is
  M4.
- Establishes an append-versioning precedent; future versioned resources should follow
  the same immutable-insert-per-edit shape rather than mutable upsert.

### Adversarial-review hardening (M1)

The mandatory security + boundary review found no High/Critical; the write path is
session + CSRF + admin-gated, the discount is server-derived, and all SQL is
parameterized. Fixes applied, each proven:

- **Money integrity at the persist seam.** `Upsert` and `SeedDefaults` reject
  negative/NaN/Inf `per_token`, a bad `reduces` base, and non-positive tier thresholds
  (`pricing.ValidateRates`), so M2 derivation is genuinely pure-data-in. The discount
  guard rejects NaN/Inf in addition to `(0,1]`.
- **`ListCurrent` shows the currently-EFFECTIVE version** (`effective_from <= now()`),
  not merely the highest version number, so the operator's "current" view matches what
  derivation bills (both use the identical rule).
- **Provenance can't be spoofed.** An API write is always stored `source=override`
  (only the seed path writes `default`); `created_by` is server-derived.
- **Contract drift guard.** Because the price-entry contract is hand-mirrored (control-
  plane metadata, not codegen'd into `pkg/model`), a reflection test asserts the schema
  properties and the `Entry`/`Rate`/`Tier` json tags are the same set bidirectionally,
  and the POST decoder rejects unknown fields — so a future schema/struct divergence
  fails the build.
- **Single-project discount seam** is marked with an explicit assumption guard: the
  project is server-derived (`DefaultProjectID`), safe today; multi-project MUST switch
  it to session→project resolution before reuse.

## Alternatives considered

- **Hardcoded price map** — rejected: the treadmill the mine names as the #1 pain.
- **Prices in ClickHouse (scale)** — rejected: append-only storage is hostile to edits,
  and it would fork the source of truth per profile; control-plane Postgres is uniform.
- **Mutable upsert + separate audit log** — rejected: re-derivability needs the exact
  historical rate row retrievable by reference, which append-versioning gives directly;
  an audit log of diffs would not let a backfill re-price against the precise old entry.
