# ADR-0026: Scale profile — ClickHouse adapter, WAL spool, dual-read

- **Status:** Accepted (Arc L — scale profile; L1 adapter foundation, L5 permanent
  dual-read + resumable backfill)
- **Date:** 2026-07-11
- **Deciders:** m7mdhka (Arc L, built against the ruled scale-mine requirements)
- **Relates to:** ADR-0007 (two profiles), ADR-0016 (canonical model), ADR-0019
  (query DSL / adapter-owned compile), ADR-0021 (explicit-clear), ADR-0025 (K1.5
  statement_timeout). Evidence: the Langfuse scale-issue mine (ClickHouse/self-hosting
  requirements report); every requirement traces to an issue their operators bled on.

## Context

The scale profile runs LLMObs at production volume: a ClickHouse storage adapter, a
durable ingest spool that closes the lite ack-window, a scale event backend, and a
permanent dual-read lite↔scale topology. The mine's three load-bearing decisions were
ruled by the maintainer; this ADR records them plus the L1 adapter-foundation
decisions. The cross-adapter meta-decision governs everything: **the ClickHouse
adapter MUST produce identical observable Query-API results to Postgres**, enforced by
conformance from the first commit.

## The three rulings (final — do not relitigate)

- **RULING-MIG6 — lite→scale is permanent dual-read, as the designed default.**
  Historical data stays on Postgres-lite, new data on ClickHouse-scale, unified at the
  Query API as a **tested invariant** (read-after-write consistency across the
  boundary, R-MIG5). Achievable because lite already stores one merged row per
  `(project,id)` — the same logical contract scale reads. A tooled Postgres→ClickHouse
  backfill is a *convenience* built after dual-read. This turns Langfuse's
  twice-revolted migration cliff into our Pillar-2 proof. (L5.)
- **RULING-CH9 — mandatory per-query resource limits, NORMATIVE + conformance-gated.**
  Every generated ClickHouse read MUST carry `max_execution_time` + `max_memory_usage`
  + `max_rows_to_read`/`max_bytes_to_read`, fail-closed (the adapter refuses to emit a
  read without them), asserted by a conformance check. `99-adapter-guidance.md`'s
  resource-limit guidance is promoted to a **normative** cross-adapter requirement:
  any adapter MUST enforce per-query resource bounds appropriate to its engine,
  conformance-verified. (L2.)
  - *Second-incumbent confirmation:* Opik (Comet, also ClickHouse-backed) ships the
    exact incidents these rulings prevent — Opik #4136 is a `MEMORY_LIMIT_EXCEEDED`
    (6 GiB) crash loading a large multi-span trace (no per-query cap), and Opik #4576
    is a start-up crash loop from hardcoded `ON CLUSTER '{cluster}'` on a single-node
    install (no `No macro 'cluster'`). RULING-CH9's fail-closed caps and R-CH1's
    threaded cluster name are the direct antidotes. (Round 1 Opik cross-over mine,
    `docs/research/opik-issues/round-01-findings.md`.)
- **RULING-SP7 — local WAL spool for the durable floor, async S3 as the archival/replay
  tier behind it (NOT S3-on-the-hot-path).** The ack becomes durable when bytes hit a
  local WAL (fast, no hot-path network, closes the SIGKILL window); a background
  flusher moves records to object storage as the durable archival + replay source.
  Preserves Arc-G's p99<50ms floor and all three guarantees (G1 drain, G2
  backpressure-via-spool-fill with the same 503-then-retry contract, G3
  tombstone-on-replay). (L3. Stop-and-surface if local-WAL-then-async-S3 complicates
  G3 in a way S3-first wouldn't.)

## L1 decisions — the ClickHouse adapter foundation

### D1 — The merge fold is extracted to a shared, engine-neutral package

The Postgres adapter's fold (`Fold`, `MergeEvent`, `Provenance`, the folder) is
already pgx-free (imports only stdlib + `storage`). It is **the canonical merge
semantics**, not a Postgres detail. It moves to `kernel/internal/storage/merge`;
both adapters import it. This is what makes the cross-adapter contract *real*: both
engines run the **identical** fold, so the only divergence risk is the SQL read/write
surface — exactly what SQL-level conformance (L2) tests. No adapter re-implements the
fold (divergence would be a silent correctness bug).

### D2 — Engine model: settled-row merge-on-write + ReplacingMergeTree, read-dedup by `(project_id,id)`

Following `99-adapter-guidance.md` §2 (v4-validated — read-time joins/dedup of
*partial deltas* is what doesn't scale):

- **Write** = merge-on-write producing a **settled row**: on each event, read the
  current settled row for `(project_id,id)`, fold it with the incoming event via the
  shared fold (D1), and **insert** the fully-merged result (insert-only; no in-place
  mutation — #10692 showed CH mutations aren't synchronously reliable).
- **Engine** `ReplacingMergeTree(event_ts)` (+ `is_deleted` for tombstones), sort key
  `(project_id, toDate(start_time), trace_id, id)` for spans / `(project_id,
  toDate(start_time), id)` traces / `(project_id, toDate(timestamp), subject_type,
  id)` scores, partition `toYYYYMM`. `project_id`-first for tenant prefix-range reads +
  per-project retention drops.
- **Read** dedups by `(project_id,id)` via `LIMIT 1 BY` / `argMax` (settled rows, so
  it's "pick latest by `event_ts`", NOT a partial-delta `FINAL` reconcile — the v4
  win). Because `start_time` is frozen and idempotency keys on `(project_id,id)` alone,
  we avoid Langfuse's date-boundary dedup hack entirely (guidance §2, LM-6).

### D3 — Dependency: `github.com/ClickHouse/clickhouse-go/v2` (Apache-2.0)

The official ClickHouse Go driver, Apache-2.0 (license-clean per the AGPL/SSPL ban;
verified against the module's `LICENSE`, and its build-reachable transitive closure —
`ch-go`, `paulmach/orb`, `shopspring/decimal`, `klauspost/compress`, `otel` — is all
Apache/MIT/BSD). It is a scale-profile storage dependency, not on the lite hot path;
recorded here per the "new dependency needs an ADR" rule.

**Pinned at `v2.42.0`, deliberately not the latest.** `v2.47.0` (and every release
after `v2.42.0`) requires Go **1.25**, but the repo pins Go **1.24** (`mise.toml`).
A repo-wide toolchain bump is a separate, deliberate decision that must not ride in
on an L1 storage PR, so L1 pins the newest clickhouse-go whose dependency closure
(driver `go 1.24.0`, `ch-go v0.69.0` `go 1.24.0`, `otel v1.39.0`) stays on Go 1.24.
When the toolchain is bumped to 1.25 in its own change, this pin can float forward.

### D4 — Migrations learn every Langfuse scar (R-CH1–8)

The ClickHouse migration runner + DDL MUST:
- **R-CH1** thread a configurable cluster name into every `ON CLUSTER` — no `default`
  literal (CI grep-guard).
- **R-CH2** be NFS/EFS-safe + idempotent — no `CREATE OR REPLACE VIEW` (use `DROP
  VIEW`+`CREATE VIEW`); all DDL `IF [NOT] EXISTS` (migration lint).
- **R-CH3** be replicated-engine-safe with reliable migration-state tracking,
  deterministic under both `Atomic`+`ON CLUSTER` and `Replicated`.
- **R-CH4** be analyzer-portable — set the `enable_analyzer` the adapter assumes; test
  both.
- **R-CH5** pin the CH version + document the supported range (never `:latest`).
- **R-CH6** ship `system.*_log` retention (disable the profiler, TTL the rest) in the
  scale deploy defaults.
- **R-CH7** URL-safe-encode credentials in the DSN (or pass out-of-band).
- **R-CH8** document + preflight the exact migration grant set; fail early naming the
  missing grant.

### D5 — Deploy: a `scale` compose/Helm profile

The scale deploy assets wire ClickHouse (+ later Redis, object storage) with the D4
defaults. Lite (Postgres-only) is unchanged; the two profiles share one Query API
(invariant #9).

## L5 decisions — permanent dual-read + resumable backfill (RULING-MIG6 made real)

RULING-MIG6 said dual-read is the permanent default and read-after-write across the
boundary is a *tested* invariant. L5 implements it. The load-bearing design choices:

### D6 — Dual-read is server-orchestrated per-dialect compilation, not a store decorator

The obvious shape — a `storage.TelemetryStore` decorator that fans each call to both
backends — is impossible for the list/aggregation paths: those methods take
**compiled SQL**, and compiled SQL is dialect-specific (`$1` for Postgres, `?` for
ClickHouse). Fanning one compiled statement to two engines sends Postgres SQL to
ClickHouse (the "mixed named/numeric parameters" corruption caught in review). So the
seam splits by path:

- **Non-SQL paths** (`Get*`, `GetTraceSpans`, `EraseSpans`, `PersistScore/Span`) live
  on a `dualstore.Store` decorator — dialect-agnostic, fan to both, unify.
- **Compiled-SQL paths** (spans/scores/traces list, aggregation) live in the query
  server's `DualRouter`: it compiles the SAME DSL doc for BOTH dialects, runs each
  backend on its own SQL, and merges via the exported `dualstore` helpers
  (`MergeOrdered`, `MergeTraces`, `MergeAggregation`). `dualstore.Store` satisfies
  `TelemetryStore` (so it can be the pipeline's write target) but its four Query\*
  methods **fail loud** — reads must route through the DualRouter.

The convergence seam (invariant #11) is the query server's `reads()` helper + the
`dualRows`/`dualAgg` branch: injected once when `SetDualStore` is called, every read
funnels through it. A prove-the-negative test (`dualseam_test.go`) installs a trap as
the single store and asserts no handler ever touches it in dual mode — catching the
Langfuse #14827 "forgotten existence check queried the old table" trap by
construction.

### D7 — Write model: writes → scale with seed-on-migrate; read dedup prefers scale

A write goes to scale. If the `(project,id)` exists ONLY in lite (an old entity
updated after cutover), its settled lite doc is replayed into scale as a synthetic
upsert FIRST (re-folding a settled doc yields itself), so scale holds the complete
entity before the new event folds in — no field is lost. Read dedup prefers scale for
any `(project,id)` in both, since a duplicated identity means it was migrated.

### D8 — Aggregation merge is exact only for summary-mergeable ops

`count/sum/min/max` merge across the straddle exactly; `avg/count_distinct/
percentile` are NOT summary-mergeable (they need raw rows). Single-store aggregation
passes through exactly. During the transitional straddle a non-mergeable aggregate
keeps scale's partial value; this is documented as a known limitation and disappears
once a project's data is single-store (e.g. after backfill).

### D9 — Backfill: convenience, resumable, on its own budget, fail-loud taxonomy

The lite→scale backfill is optional (dual-read already makes lite data readable). It
scans lite settled rows in a `(ts, project_id, id)` **total order** (strict tuple
advance — same-timestamp rows never loop, the #7117 trap), replays each into scale as
a synthetic upsert, and persists the cursor after every batch (resumable). It runs on
its OWN generous execution budget — deliberately not the interactive read timeout (a
short read timeout on a long migration is what broke v4's backfill) — with bounded
chunks, per-batch retry+backoff, and visible progress. Per the failure taxonomy
(CLAUDE.md #12): a persistent lite-read failure stops the run loud; a per-row
deterministic failure dead-letters (recorded, redrivable) so one bad row can't pin
the migration. Decoupled from boot readiness — `/readyz` never waits on it.

## Consequences

- A shared `storage/merge` package; the Postgres adapter is refactored to import it
  (no behavior change — proven by the unchanged conformance + merge tests).
- A new `storage/clickhouse` adapter (L1: schema + migrations + write side; L2: read
  compiler + fail-closed resource limits + SQL-level conformance).
- A new Apache-2.0 dependency (D3).
- `99-adapter-guidance.md` gains a normative resource-limit section (L2, RULING-CH9).
- A new `storage/dualstore` package + query `DualRouter`, gated by `CLICKHOUSE_URL`
  (L5, D6–D8). A new `storage/backfill` package + `backfill_state`/`backfill_deadletter`
  tables (migration 0014), gated by `BACKFILL_ON_BOOT` (L5, D9). The never-strand
  guarantee is documented in `docs/self-hosting/scaling-lite-to-scale.md` and proven
  by `dualread_test.go`, `dualseam_test.go`, and the backfill integration suite.

## Deferred / later arc

- The `docs/research/langfuse-study/` teardown book that informed the model is **not
  committed** (lives in conversation history). Flag for a later reconstruct-and-commit
  so the "why" is in-repo. Not built in Arc L.
