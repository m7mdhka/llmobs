# Opik Cross-Over Report — Round 2

> Read-and-report competitive analysis: LLMObs code vs. `comet-ml/opik`.
> Read-only against Opik; report-only. Older window (created ~2025-07 → 2025-12).
> Generated 2026-07-11. Rulings banked (§8).

## 1. Round header

| | |
|---|---|
| **Round** | 2 of N |
| **Window mined** | merged PRs #7091→#7248 (`merged:<2026-06-26`); closed issues #2769→#4137 (`created:<2025-11-20`) |
| **Items triaged** | ~240 (120 PRs + 120 issues) |
| **Resume cursor (Round 3)** | merged PRs `merged:<2026-06-15` (before #7091); closed issues `created:<2025-07-21` (before #2769) |
| **Cross-over rate** | ~5.4% (13/240) — flat vs Round 1 (~5.6%) |

5-way split: IRRELEVANT ~200 (optimizer/SDK churn, hacktoberfest UI FRs, spam),
ALREADY-HANDLED ~8, IMMUNE-by-design ~5, CROSS-OVER ~13, CONFIRMS ~2.

## 2. Cross-over findings (sorted by our-impact)

| # | Opik item | What it is | Our seam | Class |
|---|---|---|---|---|
| 1 | PR #7148 | 429 `insufficient_quota` retried forever (all-429-retryable) → error storm | invariant-12 taxonomy / WAL D6 | 📘 design-rule |
| 2 | PR #7137 | audio input tokens (16× text) billed at text rate | 06 §7.4 (generalize) | 📘 + 🟢 |
| 3 | #2782 | span persisted but trace lost (ADK/Vertex race) | trace = derived view (DSL §4.1) | 📄 positioning (IMMUNE) |
| 4 | #3397 | usage build crashes on `*_details=None` | normalizer null-handling (§9.1) | 🟢 fixture |
| 5 | PR #7205 | created_at skewed by non-UTC JVM host | timestamp read path (both adapters) | 📘 verify-now |
| 6 | PR #7091 | fast-fail 503 on pool saturation | G2 backpressure | 📘 confirm |
| 7 | #3172/#3305 | vanished upstream image breaks self-host | airgap bundle | 📄 docs |
| 8 | PR #7093 | MCP OAuth dynamic client registration (RFC 7591) | agent/plugin auth (#21) | 🔵 |
| 9 | #3327 | group-scoped workspace + per-user isolation | tenancy/RBAC (#21) | 🔵 |
| 10 | PR #7144/#7123 | Cost Intelligence gated on entitlement | ungated-core wedge | 📄 positioning |
| 11 | #3441 | map OTel attrs (thread_id) → Threads | session grouping fixture | 🟢 |
| 12 | PR #7107 | dropped unused perl to clear a CVE | image hygiene | 🟡 |
| 13 | #3291/#3783/#3089/#4033 | sub-path ingress, custom labels, ext-secret-store | Helm-values coverage | 📄 docs |

## 3. CONFIRMS
- **Invariant 12** — PR #7148: a second incumbent shipped exactly the retry-forever
  bug the invariant warns about, then fixed it by sub-classifying the 429. Proof the
  invariant is real.
- **§7.4** — PR #7137 (audio at 16×) generalizes Round-1 cache findings: the rule is
  *every* detail key, not cache-only.

## 4. Fixtures harvested
- #3397 null usage-detail objects → normalize as absent buckets, no error.
- PR #7137 audio span → audio rate on audio tokens, input rate on residual.
- PR #7205 non-UTC host → identical timestamps to UTC host.
- #3441 OTel thread_id/session attr → canonical session grouping.
- PR #7148 429-quota → no retry; 429-ratelimit → retry.

## 5. #21 auth harvest
- PR #7093 MCP OAuth dynamic client registration (RFC 7591) — ties ADR-0025 R5.
- #3327 group-scoped workspace + per-user project/dataset isolation.

## 6. Round bottom line
Top 3: (1) #7148 confirms invariant-12 → sub-classify within a status code; (2) #7137
→ generalize §7.4 to all detail keys; (3) #2782 → trace-as-derived is structurally
immune (strongest positioning finding). Cross-over flat & low (~5.4%). Round 3
(pre-2025-07) is the closeout — expect scale/self-hosting-heavy, cost-light, and a
likely collapse toward zero (which is itself the finding).

---

## 7. Verify-now result — #7205 timezone (completed this turn)

**Finding: immune to the corrupting form of #7205; connection is not explicitly
UTC-forced (defense-in-depth gap only).**

- Opik #7205 was a MySQL `DATETIME` (tz-naive) read where the JDBC driver applied the
  JVM's offset and **shifted the instant** — genuine corruption.
- **Postgres (lite):** all timestamp columns are `TIMESTAMPTZ`
  (`migrations/0005_scores.sql:15,21`, `0004/0009`), which store an absolute instant;
  pgx transfers the instant over the binary protocol, so a non-UTC host does **not**
  shift it. `NewPGPool` (`kernel/internal/platform/postgres.go:11`) does not set a
  session `timezone`, so a returned `time.Time` may carry a non-UTC `Location()`, but
  the instant is correct and every format/compare path calls `.UTC()`
  (`postgres/erase.go`, `backfill.go`).
- **ClickHouse (scale):** columns are `DateTime64(6)` (`migrations/0001_init.sql:26-28`),
  transferred as ticks-since-epoch (absolute); the instant is preserved regardless of
  client/host tz. `ver` (write-order), not a wall-clock, drives versioning (N1), and
  erasure suppression checks `now64()` server time — none depend on host tz.
- **Conclusion:** the #7205 *shift* class cannot corrupt our stored instants — immune
  by column-type choice, not by connection-forcing.

**Recommended follow-up (tracked, not done this turn — needs DB containers + a
TZ-mutated conformance run I can't prove green here):**
1. Defense-in-depth: set `cfg.ConnConfig.RuntimeParams["timezone"]="UTC"` on the pg
   pool and `session_timezone='UTC'` (or a UTC `Location`) on the CH client, so
   returned `time.Time` is UTC regardless of host — removes reliance on every caller
   remembering `.UTC()`.
2. Conformance assertion: run the cross-adapter suite with the harness host `TZ` set
   to a non-UTC zone (e.g. `America/New_York`) and assert byte-identical timestamps to
   the UTC run.

## 8. Rulings (Round 2 dispositions — banked 2026-07-11)

| Finding | Ruling | Banked into |
|---|---|---|
| #7148 429 sub-classification | **ADOPT** design-rule + fixture | ADR-0027 D6; issue-13 §3a |
| #7137 audio → generalize §7.4 to all detail keys | **ADOPT** design-rule + fixture | `06-usage-cost.md` §7.4 (rewritten to general form); issue-13 §3a (meta-lesson) |
| #7205 timezone | **ADOPT verify-now** — done (§7); follow-up tracked | this doc §7; issue-13 §3a ref |
| #3397 null detail objects | **ADOPT** fixture | issue-13 §3a |
| #2782 span/trace race | **CITE** positioning (structurally immune) | `docs/positioning.md` |
| #7091 503 saturation | **CITE** — confirms G2 | positioning |
| #7144/#7123 entitlement gating | **CITE** — third plan-gating data point | positioning |
| #3172/#3305 vanished image | **BANK** airgap checklist | `.claude/rules/deploy.md` |
| #3291/#3783/#3089/#4033 Helm gaps | **BANK** Helm-values checklist | deploy.md |
| #7107 image hygiene | **BANK** low-priority audit | deploy.md |
| PR #7093 MCP OAuth RFC 7591 | **BANK** into #21 | `issue-21-auth-rbac-design-notes.md` §7 |
| #3327 group-workspace isolation | **BANK** into #21 | issue-21 notes §7 |
| Compose #69 | **DEFER** — later PR with the e2e fix in the same change | GitHub #69 |

_No `make lint`/`test` run: Round-2 banking is prose/spec/fixture-doc only, no codegen
impact. Compose #69 stays deferred._
