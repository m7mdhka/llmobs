# ADR-0016: Canonical data model `v1alpha1`

- **Status:** Accepted
- **Date:** 2026-07-09
- **Deciders:** m7mdhka (data model design session)

## Context

The canonical data model is bootstrap step 3 and the most load-bearing contract
in the repository: it is the storage-neutral logical model every ingestion
normalizer maps *into*, the Query API reads *from*, and every plugin consumes
(invariant 5, "contracts first"). Nothing downstream — storage adapters,
normalizers, the DSL planner, the SDK — can be designed until it exists.

Before designing it we conducted a deep teardown of **Langfuse**, the
most-deployed open-source LLM observability platform, whose schema survived
production scale and a v2→v3 architectural migration (Postgres-only → ClickHouse
+ Redis + S3). That study (the Langfuse teardown study; `feature/docs-langfuse-study`,
chapters and `findings-for-data-model.md`) is the evidence base for this model.
The data-model design session concluded with **twelve locked decisions**
(LM-1..LM-12). This ADR records the model and links each decision to its
normative specification.

## Decision

We adopt the canonical data model **`v1alpha1`**, specified normatively in
[`api/model/v1alpha1/`](../../api/model/v1alpha1/). The model defines exactly five
entities — **Trace**, **Span**, **Score**, **ScoreConfig**, **MediaReference** —
and deliberately excludes sessions/users/threads (dimensions, not entities),
datasets/runs/run-items (evals plugin), and annotations/corrections (future
plugin concept).

### The twelve decisions

| # | Decision | Spec |
|---|---|---|
| LM-1 | Closed canonical `kind` enum (7 kinds) + `raw_kind`; three payload shapes `point_event ⊂ timed_span ⊂ generation_shaped`; additive-only new kinds; foreign-type mapping policy. | [`02-span.md`](../../api/model/v1alpha1/02-span.md) §2–3 · ADR-0018 |
| LM-2 | Fixed promoted field set; everything else in `attributes`; raw attributes always preserved; promotion additions require an ADR. | [`02-span.md`](../../api/model/v1alpha1/02-span.md) §1 · ADR-0018 |
| LM-3 | A score is a measurement only; `data_type ∈ {numeric, categorical, boolean}`; both value fields nullable, no sentinels; sources `{human, llm_judge, code, external}`; feedback/corrections are a future plugin concept. | [`04-score.md`](../../api/model/v1alpha1/04-score.md) · ADR-0017 |
| LM-4 | Dual usage/cost maps (provided vs resolved); provided cost wins; `cost_source` tag + `pricing_snapshot_ref`; re-pricing is a `jobs` backfill. | [`06-usage-cost.md`](../../api/model/v1alpha1/06-usage-cost.md) |
| LM-5 | Versioned re-insert + tombstone semantics; the **merge algorithm is normative** (empty-never-clobbers, deep-merge, tags union, latest-`event_ts`-wins); frozen fields. | [`05-update-semantics.md`](../../api/model/v1alpha1/05-update-semantics.md) |
| LM-6 | `id` is the idempotency key; `start_time` frozen (mismatch normalized, not applied); no date-boundary delay mechanisms. | [`01-entities.md`](../../api/model/v1alpha1/01-entities.md) §3 · [`05`](../../api/model/v1alpha1/05-update-semantics.md) §5 |
| LM-7 | Sessions/users/threads are derived-by-grouping dimensions, not entities; multi-user sessions allowed; UI flags are plugin `kv`. | [`01-entities.md`](../../api/model/v1alpha1/01-entities.md) §4.1 |
| LM-8 | Score subjects are `(subject_type, subject_id)`; kernel types `span`/`trace`/`session`; plugins register namespaced types; datasets are a plugin. | [`04-score.md`](../../api/model/v1alpha1/04-score.md) §5 · ADR-0017 |
| LM-9 | Logical spec is storage-neutral; physical layout is **informative** adapter guidance only. | [`99-adapter-guidance.md`](../../api/model/v1alpha1/99-adapter-guidance.md) |
| LM-10 | Media out-of-band via reference tokens + `(project, sha256)` dedup; truncation only with an observable flag; opaque `input`/`output`. | [`02-span.md`](../../api/model/v1alpha1/02-span.md) §7 · [`08`](../../api/model/v1alpha1/08-data-quality.md) |
| LM-11 | `environment`/`release`/`version` dimensions; one sanitization routine in the shared `normalize` stage for every transport; no silent coercion. | [`02-span.md`](../../api/model/v1alpha1/02-span.md) §4.2 · [`08`](../../api/model/v1alpha1/08-data-quality.md) §2 |
| LM-12 | Cross-boundary references are `(type, id)` + optional label snapshot; no FKs; dangling-tolerant. | [`07-references.md`](../../api/model/v1alpha1/07-references.md) |

### Meta-decision (storage-neutral spec + cross-adapter conformance)

The model is a **logical** specification describing *observable semantics only*.
Two adapters — lite (Postgres-only) and scale (ClickHouse) — MUST produce
identical Query-API-observable behavior for the same event sequence. The
conformance suite (`tools/conformance`) will assert this against both adapters,
using the merge test vectors ([`05-update-semantics.md`](../../api/model/v1alpha1/05-update-semantics.md) §6)
as the core cases. No physical representation may become observable.

## Consequences

**Positive.**
- Normalizers, the Query API, and plugins now have a single, versioned,
  storage-neutral contract to build against (unblocks bootstrap steps 4–6).
- The append-only + read-time-reconciliation model that survived Langfuse's scale
  is captured as a storage-neutral fold, so lite and scale share one semantics.
- Raw attributes are always preserved (invariant 6); no ingested data is lost.
- Silent lossy behaviors that the study flagged as regrets (silent environment
  coercion, silent async score drops, unmarked truncation, dedup-key-driven
  duplicate rows) are eliminated by design.

**Negative / costs.**
- The promoted set is a near-permanent commitment (adding a field later needs an
  ADR and does not backfill history) — see [`02-span.md`](../../api/model/v1alpha1/02-span.md) §1.1.
- Storage-neutrality forbids leaking physical optimizations into the contract;
  adapters must carry more logic (e.g. the scale adapter dedups on `(project_id,
  id)`, not its sort key).
- Cross-boundary integrity is by convention, so consumers must tolerate dangling
  references and the kernel must run cascade/backfill jobs.

**Neutral.**
- This is `v1alpha1`. Within a major, changes are additive-only; breaking
  changes require a new maturity version + deprecation window (VERSIONING.md).

## Alternatives considered

- **Re-export OTel spans as the model.** Rejected: OTel leaves GenAI concepts
  (kinds, generation payloads, usage/cost, scores) in attributes; the Query API
  needs them as first-class fields. We instead normalize *into* an OTel-shaped
  model (invariant 6) while promoting GenAI concepts.
- **Adopt Langfuse's schema directly.** Rejected: the study documented concrete
  regrets we choose not to inherit (sentinel score values, overloaded score
  types, dedup identity = physical sort key, transport-divergent normalization,
  silent coercion). Each divergence is recorded in the spec's `> Evidence:` blocks
  and in ADR-0017/0018.
- **Storage-coupled model (design directly for ClickHouse).** Rejected: violates
  the lite/scale dual-profile invariant (invariant 9). We keep the model logical
  and put physical guidance in an informative appendix.

## Links

- Specification: [`api/model/v1alpha1/`](../../api/model/v1alpha1/) (files `00`–`08`, `99`, `schema/`).
- Sub-decisions: ADR-0017 (score model), ADR-0018 (span taxonomy and payload shapes).
- Evidence base: the Langfuse teardown study (`feature/docs-langfuse-study`,
  `docs/research/langfuse-study/findings-for-data-model.md` and chapters 03, 05, 06, 07).
- Relates to design decisions D3 (OTLP-canonical), D8 (SDK primitives / data nouns),
  D9 (typed query DSL), D11 (K8s-style versioning) — see the repository design doc.
