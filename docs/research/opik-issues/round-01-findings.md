# Opik Cross-Over Report — Round 1

> Read-and-report competitive analysis: LLMObs code vs. `comet-ml/opik` (Comet's
> open-source LLM-observability platform). Read-only against Opik; report-only —
> no issues opened, no code written, nothing changed. One round per invocation.
> Generated 2026-07-11.

---

## 1. Round header

| | |
|---|---|
| **Round** | 1 of N |
| **Repo (verified)** | `comet-ml/opik` — Comet-ML, 20,531★, Java/Dropwizard backend + ClickHouse + MySQL + React SDK/UI. Probe confirmed owner/name. |
| **Pages mined** | 5 (≈125 merged PRs #7254→#7426 + ≈125 closed issues, newest-first) |
| **Resume cursor (next round)** | merged PRs `merged:<2026-06-26` (before #7254); closed issues `created:<2025-11-20` (before #4136). Feed both to Round 2. |
| **Items triaged** | ~250 surfaced; triaged to the 5-way split below |

**5-way triage split** (headline: **cross-over rate is low, as expected — and the low rate is concentrated in exactly one seam we haven't built yet**):

| Bucket | Count (approx) | Notes |
|---|---:|---|
| IRRELEVANT | ~205 | Their-stack: Java/Dropwizard internals, their React UI, their Python/TS SDK maintenance, model-price-file syncs (#7425/#7394/#7349…), CI/QA flake fixes, ADK/DSPy/LangChain SDK-instrumentation churn, "test"/spam issues (#4341/#4343/#4348/#4359…). |
| ALREADY-HANDLED | ~9 | CH resource OOM, CH cluster-macro migration, unbounded scans — our Arc-L caps + migration templating already cover. |
| IMMUNE-by-design | ~6 | Cross-language trace propagation (#5127), token-count-as-dollars (#5619), no-SDK treadmill. |
| **CROSS-OVER** | **~14** | The table in §2. |
| CONFIRMS-LANGFUSE | 3 | Aggregate usage double-counting, cache-cost mishandling, cost-without-usage — see §3. |

**Cross-over rate: ~5.6% (14/250).** Honest and low. Almost the entire genuine
harvest lands on **one seam: the unbuilt cost-derivation / enrich stage**
(`pipeline/stages.go` no-op; `api/model/v1alpha1/06-usage-cost.md` §7; issue #13).
That is a *valuable* concentration — Opik has independently walked into the exact
bug field our §7.1–7.3 anti-rules were written for, plus several new ones we
hadn't enumerated.

---

## 2. Cross-over findings table (sorted by our-impact)

Classification legend: 🔴 pre-launch fix · 🟡 adoptable pattern · 🟢 fixture to add · 📘 design-rule to pin · 📄 docs · 🔵 #21-auth-harvest

| # | Opik item | What it is | Why it crosses to us (our seam) | Class | Recommended action |
|---|---|---|---|---|---|
| 1 | **#5618** LiteLLM OTel: cache tokens billed at full input price | Cache-read tokens charged at full input rate → 5–10× over-report on long-context caching | Our cost-derivation is **unbuilt**; when we build it, F3 (`06-usage-cost.md` §3.1) says cache is additive detail — but F3 alone doesn't *price* cache-read at the discounted rate. The price table needs `cache_read_input_token_cost` per provider. | 📘 design-rule | Pin a rule in `06` §7: derivation MUST price `cache_read`/`cache_write` detail keys at their own rate, never fold them into input at input rate. |
| 2 | **#6976** Gemini/Google cached tokens not discounted | Same as #1 but provider-specific: cache calculator registered for anthropic/openai/bedrock, **missing for google_vertexai/google_ai** → silent fallback to flat cost | Our future derivation must not have a **per-provider allow-list of "who gets cache pricing"** — that's the exact drift trap (invariant 11: enforce at the seam, not per-provider). | 📘 design-rule | Rule: cache pricing is data-driven from the price entry's presence of a cache rate, **never** a hardcoded provider set. One code path, all providers. |
| 3 | **#5621** LiteLLM model names with provider prefix (`openai/gpt-4o`) → zero cost | Price map stored keys stripped of prefix at load, but lookup used raw prefixed name → all 4 fallbacks miss → `cost=0`, dashboard blank | Directly hits our model-identity handling: `semconv.go:137-144` keeps served model + `llmobs.raw.model_requested`. When derivation looks up price, prefixed/namespaced names (`openai/`, `vertex_ai/`, `openrouter/openai/`) must normalize **identically at load and lookup**. | 🟢 fixture + 📘 | Harvest fixtures: `openai/gpt-4o`, `openrouter/openai/gpt-3.5-turbo`, `anthropic/claude-3-5-sonnet-20241022`. Assert price-key normalization is symmetric. |
| 4 | **#4508** DSPy cache hit: no tokens, no time, but **cost calculated** | Cache-hit span has zero usage yet non-zero derived cost | **Our §7.3** ("model-without-usage MUST NOT fabricate cost") is the exact anti-rule — but it's **designed, not enforced** (enrich no-op). | 🟢 fixture | Add a conformance vector: model set + usage absent ⇒ `cost_source=null`, `total_cost` empty. Wire into the enrich stage's first test. |
| 5 | **#6982** Tiered (>200k) pricing not applied | `*_above_200k_tokens` rate fields never parsed; flat base rate always → long-context under-report | New failure mode our `06` doesn't yet enumerate: **threshold/tiered pricing**. Our `pricing_snapshot_ref` design must capture tier boundaries, or re-pricing backfill will be wrong. | 📘 design-rule | Add to `06` §4/§7: price entry may carry tier thresholds; derivation applies per-tier; snapshot ref records the tier schedule version. |
| 6 | **#6344** Automation-rule filter fields **accepted by API, silently ignored** by evaluator | `error_info` (and others) round-trip through create/list/read but the online-scoring evaluator drops them with only a WARN log | Crosses to our **DSL field contract**: `api/query/v1alpha1/fields.json` is the single source; a field the schema accepts must be evaluable on **every** path (invariant 11 — one convergence seam). Silent-accept-then-ignore is precisely the `08-data-quality.md` "no silent lossy behavior" violation. | 📘 design-rule | Confirm: any field in `fields.json` is rejected at validate-time on paths that can't evaluate it — never accepted-then-dropped. Worth a conformance assertion. |
| 7 | **#5596** Hardcoded DB password in Helm `values.yaml:657` | CRITICAL: literal `DATABASE_PASSWORD` shipped in the production chart | **Pre-launch check ran** (§4): our **Helm chart is clean** (no literal secrets found; uses refs). But `deploy/compose/{lite,scale}.yaml` ship literal `POSTGRES_PASSWORD: llmobs` / `CLICKHOUSE_PASSWORD: llmobs` (not env-overridable), and bootstrap creds as `${VAR:-admin-dev-password}` defaults. | 🟡 adoptable / 📄 | Compose literals are dev-only but should read `${POSTGRES_PASSWORD:?}` (required, no default) so a copy-paste to prod fails loudly. Small change, high signal. |
| 8 | **#4136** ClickHouse OOM (6 GB) on 3k-span traces | `MEMORY_LIMIT_EXCEEDED` loading a large trace; expected 10k spans | **ALREADY-HANDLED**: our RULING-CH9 mandatory per-query caps (`MaxMemoryUsage`, `MaxRowsToRead`, `MaxBytesToRead`) fail-closed via `readGuard()` (`store_read.go:12-47`). Confirms the design was right. | 📘 (confirm) | No action — but add a scale-fixture: a synthetic 10k-span trace query must return bounded or `ceiling_exceeded`, never OOM. |
| 9 | **#4576** CH migration: hardcoded `ON CLUSTER '{cluster}'` → `No macro 'cluster'` crash-loop on single-node | Changesets 46–49 hardcode cluster macros; also Liquibase id mismatch; no migration-recovery tooling | **ALREADY-HANDLED**: our R-CH1/2/3 migration templating `{{on_cluster}}` never-literal-cluster + functional grant preflight (`preflight.go`). Strong confirmation this hazard is real. | 📄 docs / 📘 | No code action. Note in Arc-L docs as the concrete incident our templating prevents. Their "no recovery tools → CrashLoopBackOff" is a caution for our migrate UX. |
| 10 | **#6761** `opik_args` span merge **drops** `environment`/`thread_id` | Merge helper manually rebuilds params copying only a subset → later-added fields silently lost | Their bug is SDK-side, but the *class* — a partial-object merge that drops unlisted fields — is exactly what our **V17 "object-valued scalars replace wholesale"** rule must not do to sibling fields. Check `05-update-semantics.md` §3 handles partial payload objects field-group-wise, not whole-entity-wise. | 📘 design-rule | Verify our field-group fold can't drop an unspecified field when a partial object update lands. Likely already immune (per-field-group), but pin a vector. |
| 11 | **#6928** Vertex AI model picker routes to `provider='gemini'` instead of `vertex_ai` | Provider mis-canonicalization → wrong credentials, wrong pricing | Crosses to provider-identity normalization (`semconv.go:145`). We promote `gen_ai.provider.name`/`gen_ai.system` verbatim — good — but derivation-time provider canonicalization (gemini vs vertex_ai vs google_ai) must be one table. | 🟢 fixture | Harvest provider-alias fixtures (vertex_ai/gemini/google_ai; azure vs openai) for the future derivation provider-canon map. |
| 12 | **#5619** `getCostFromMetadata()` reads `total_tokens` count as USD | Token count (150,000) returned as $150,000 in a fallback path | **IMMUNE-by-design**: our dual maps keep `provided_cost_details` (typed decimals ≥12 digits) strictly separate from usage counts (`06` §1). A token count can never be read as a cost. Good confirmation of the dual-map design. | 📘 (confirm) | No action. Cite as a dual-map win. |
| 13 | **#4483** Spans collected under **incorrect/empty traces** (concurrent eval threads) | Broken parentage: spans land under wrong trace, some traces empty; count == task-thread count → thread-local context bleed | Their SDK bug, but the *input shape* (orphaned/mis-parented spans) is our fixture obligation: orphan-promotion + `incomplete_trace`/`is_open` (DSL §4.2). | 🟢 fixture | Harvest a mis-parented/orphan-span fixture; assert orphan promotion + `incomplete_trace` marker, no cross-trace bleed. |
| 14 | **#6614** FR: native vendor-neutral `gen_ai.*` OTEL semconv support | Users want Opik to ingest raw OTel GenAI without the Opik SDK | **Positioning confirmation, not a bug**: this *is* our canonical path (`otel-genai` normalizer is the only + fallback dialect). A top incumbent's users explicitly asking for what we made canonical. | 📄 positioning | No action — evidence for `docs/positioning.md` pillar (OTLP-canonical). |

---

## 3. CONFIRMS-LANGFUSE (both incumbents independently hit — strong "be demonstrably immune" signal)

| Bug class | Opik (this round) | Langfuse (catalogued) | Our immunity status |
|---|---|---|---|
| **Aggregate/root span double-counts leaf usage** | **#4695** — root trace usage exactly 2× the sole consuming span (LangGraph + Pydantic AI OTel) | #14808 (agent-span usage zeroed/double) | **Designed, not enforced.** `06` §7.1 forbids double-count; enrich stage unbuilt. **Highest-value fixture this round** — see §5. Two independent incumbents hitting aggregate-usage math is the strongest signal in the round. |
| **Cache-cost mishandled** | **#5618/#6976/#6969** — cache-read billed at full input rate (Opik *under*-discounts) | #14902/#14945 (Langfuse *over*-subtracts) | Both get cache wrong, in **opposite directions** — proof the surface is genuinely hard. F3 (as-is verbatim) + a cache-rate-priced derivation immunizes us **if** we pin design-rules #1/#2 above. |
| **Cost fabricated without usage** | **#4508** — cache hit, zero tokens, non-zero cost | #14945 (model-set flips to estimated-then-priced) | §7.3 anti-rule exists; enforce it in the enrich stage's first commit (fixture #4 above). |

---

## 4. Pre-launch shortlist (this round)

Honest and small — only one item, and it's minor:

- 🟡 **Compose dev credentials are silently defaultable** (`deploy/compose/lite.yaml:11`,
  `scale.yaml:16/31`, bootstrap creds `lite.yaml:27-29` / `scale.yaml:70-72`).
  Unlike Opik's #5596 (literal secret in the *production Helm chart*), **our Helm
  chart is clean** — this is dev-compose only. But `POSTGRES_PASSWORD: llmobs` /
  `CLICKHOUSE_PASSWORD: llmobs` are hardcoded literals and
  `${LLMOBS_BOOTSTRAP_ADMIN_PASSWORD:-admin-dev-password}` provides a working
  default. **Fix sketch:** switch prod-facing values to `${VAR:?err}` (required,
  no default) so a copy-paste to a real deployment fails loudly rather than
  booting with a known password. Not a blocker; a hardening.

No cost/query/auth code bug found in our tree this round — largely because the
seam Opik is bleeding on (cost derivation) is **not built yet** in our kernel.
That is the honest headline.

---

## 5. Fixtures harvested (for the normalizer / conformance / future-enrich suites)

| Source | Fixture | Assertion |
|---|---|---|
| **#4695** | LangGraph + Pydantic AI OTel trace: parent/agent span + one leaf generation span carrying usage | Trace-level / aggregate usage MUST equal leaf usage, **not** 2× (§7.1). **Priority — dual-incumbent-confirmed.** |
| #5621 | Spans with `gen_ai.request.model` = `openai/gpt-4o`, `openrouter/openai/gpt-3.5-turbo`, `anthropic/claude-3-5-sonnet-20241022` | Price-key normalization symmetric at load & lookup; no zero-cost from prefix miss. |
| #4508 | Model set, usage absent (cache hit) | `cost_source=null`, `total_cost` empty — no fabricated cost (§7.3). |
| #6976/#5618 | Any provider with a cache-read rate in its price entry + `cached_tokens` present | Cache-read priced at its own rate, data-driven, no per-provider allow-list. |
| #6982 | `gemini-2.5-pro` call >200k tokens with tiered price entry | Tokens above threshold billed at tier rate; snapshot ref records the tier schedule. |
| #4483 | Mis-parented / orphan spans across concurrent contexts | Orphan promotion + `incomplete_trace`; no cross-trace span bleed. |
| #6928 | Provider aliases: `vertex_ai` vs `gemini` vs `google_ai`; `azure` vs `openai` | One provider-canonicalization table; verbatim raw preserved (`semconv.go:145`). |

---

## 6. #21 auth harvest (second-incumbent angle)

Thin this round — most Opik auth activity was internal refactors, but three
patterns worth feeding issue #21:

- **PR #7259 / #6976-#6977** "Permissions follow-up: Agent Playground, Online
  Evaluation, Alerts, Prompt Library" — Opik is retrofitting per-**surface**
  permission gating onto features that shipped ungated. Reinforces our design:
  permission intersection at the **Query API seam** (invariant 11), so a new
  plugin surface inherits gating by construction rather than a per-feature
  retrofit. 🔵
- **PR #7368/#7253** "request identity encoding on all react-service auth calls"
  — they had to fix identity propagation across *every* auth call site
  (per-caller drift). Second-incumbent evidence for our double-token "compute
  intersection at one seam, never per-caller" rule. 🔵
- **#6281** "evaluate calls the Comet cloud instance instead of the self-hosted
  instance" + enterprise-auth gating context — confirms the #21 wedge: Comet
  gates enterprise auth *and* has cloud-vs-self-hosted identity ambiguity. Feed
  as a second data point (alongside Langfuse's `admin-api` gating) that
  ungated-by-plan / strictly-gated-by-authz is the differentiator. 🔵

No SSO/SAML/OIDC/SCIM PRs surfaced in this window (Opik gates those behind
Enterprise, so they're largely closed-source) — will watch for leaked
config/model-layer PRs in later rounds.

---

## 7. Round bottom line

**Top 3 by our-impact:**

1. **The cost-derivation bug field (#5618/#6976/#6982/#5621/#4508)** — Opik is
   bleeding on the exact stage we haven't built. Harvest is a ready-made
   **design-rule + fixture checklist** for our enrich stage (issue #13): cache
   priced at its own rate (data-driven, no provider allow-list), tiered pricing,
   symmetric model-key normalization, no cost without usage. This is the round's
   real payload.
2. **#4695 aggregate usage doubled** — CONFIRMS-LANGFUSE #14808. Two independent
   incumbents fail aggregate-usage math ⇒ our §7.1 immunity must be
   *demonstrably* enforced with a dual-sourced fixture, not just designed.
3. **#4576 + #4136 (CH migration cluster-macros + OOM)** — both ALREADY-HANDLED
   by R-CH1/2/3 templating and CH9 fail-closed caps. Confirmation our Arc-L
   requirement set anticipated real production incidents; worth citing in docs,
   no code change.

**Next round worth running?** **Yes.** Cross-over rate is low (~5.6%) but
*concentrated and actionable* — and we've only mined the newest 5 pages (down to
~2025-11-20 for issues). Older windows likely hold the pre-2.0 ClickHouse-scale
and self-hosting pain (the #4136/#4576 class) plus early cost-table history.
Recommend Round 2 from the recorded cursor, with the same high bar.

---

## Appendix — resume state for Round 2

- **Merged PRs cursor:** `gh pr list -R comet-ml/opik --state merged --search "merged:<2026-06-26" ...` (continue before #7254).
- **Closed issues cursor:** `gh issue list -R comet-ml/opik --state closed --search "created:<2025-11-20" ...` (continue before #4136).
- **Method reminder:** 5 pages/round (~125 items each side), newest-first within the window; triage 5-way; deep-read only CROSS-OVER + CONFIRMS-LANGFUSE; report-only; record the next cursor; stop for human rulings.

*Round 1 status: report-only. No issues opened, no code written, no repo files changed (this report lives under `tmp/`, outside the tracked source tree).*

---

## 8. Rulings (Round 1 dispositions — banked 2026-07-11)

Decision-maker rulings on the findings above. Report-only research banked into
durable homes; **enrich stage NOT built now** — rules pinned, code is future.

| Finding | Ruling | Banked into |
|---|---|---|
| #5618/#6976 cache own-rate + data-driven | **ADOPT** design-rule | `06-usage-cost.md` §7.4; `issue-13-cost-derivation-design-notes.md` |
| #6982 tiered pricing + snapshot schedule | **ADOPT** design-rule | §7.5; issue-13 notes |
| #5621/#6928 symmetric model-key + provider-canon table | **ADOPT** design-rule + fixture | §7.6; issue-13 notes |
| #4508 no cost without usage | **ADOPT** as conformance vector | §7.3 (already) + issue-13 §2 |
| #4695 aggregate usage doubled (CONFIRMS Langfuse #14808) | **PRIORITY FIXTURE** — must be demonstrably tested | §7.1 note; issue-13 §2 (standing vector) |
| Cache over-subtract (LF) vs under-discount (Opik) | Bank as F3 worked example | §7 cross-adapter note; issue-13 §1 |
| #6344 fields.json accept-then-silently-ignore | **ADOPT** design-rule/conformance assertion | see §9 below; relates to issue #5 |
| #6761 partial-object merge drops sibling field | **VERIFY** — pin a vector (likely immune) | issue-13 §4 |
| #5596 → our compose default creds | **ADOPT** (small) → tracked hardening issue; Helm already clean | GitHub issue (filed); see §10 |
| #4136/#4576 CH OOM + cluster-macro crash | **CITE, no code** — incidents our CH9 caps + R-CH1 templating prevent | ADR-0026 (Arc-L) |
| #5619 token-count-as-USD | **CITE** — dual-map immunity win | issue-13 §3 |
| #6614 native gen_ai.* OTEL | **CITE** — OTLP-canonical positioning | docs/positioning.md (pillar evidence) |
| Auth harvest (#7259, #7368/#7253, #6281) | **BANK** into #21 | `issue-21-auth-rbac-design-notes.md` §7 |

## 9. #6344 — fields.json validate-time rejection (design-rule)

An automation-rule filter field accepted by Opik's API is silently ignored by
the evaluator (only a WARN log). Our rule: **any field present in
`api/query/v1alpha1/fields.json` MUST be either evaluable on a given path or
REJECTED at validate-time on that path — never accepted-then-silently-dropped**
(`08-data-quality.md` "no silent lossy behavior"; invariant 11 — one convergence
seam). Prove it with a conformance assertion: for every field × every query
path, an accepted field is evaluated or the query is refused. Relates to issue
#5 (SDK prevalidation generated from fields.json).

## 10. Compose hardening (#5596 analog) — filed, not yet edited

Our Helm chart is clean. `deploy/compose/{lite,scale}.yaml` ship literal
`POSTGRES_PASSWORD: llmobs` / `CLICKHOUSE_PASSWORD: llmobs` and a defaulted
bootstrap admin password. The `${VAR:?err}` (no-default, fail-loud) switch is the
right hardening, but the documented invocation is a bare
`docker compose -f … up` with no `.env`/`--env-file`, so the switch must be made
*together with* a committed dev env-file + Makefile/CI `--env-file` wiring, or it
hard-fails `make dev` and the release-blocking kind e2e. Tracked as its own
issue rather than ridden into this research turn.
