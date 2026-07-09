# ADR-0018: Span taxonomy and payload shapes

- **Status:** Accepted
- **Date:** 2026-07-09
- **Deciders:** m7mdhka (data model design session)
- **Parent:** ADR-0016

## Context

The span is the workhorse entity. Its taxonomy (how many types, open or closed)
and its field profile (what every span carries vs. what only generations carry)
are the decisions that most shape the Query API and the normalizers. This ADR
records decisions **LM-1** (taxonomy + payload shapes) and **LM-2** (promoted
set). Normative spec: [`02-span.md`](../../api/model/v1alpha1/02-span.md).

## Decision

1. **`kind` is a closed canonical enum** of seven values: `span, generation,
   embedding, tool_call, retrieval, agent_step, guardrail`. New kinds are
   additive-only within a major; an unknown kind is treated as `span` (forward
   compatibility).
2. **`raw_kind` always preserves the original source type** (e.g. Langfuse
   `CHAIN`, OpenInference `LLM`). The canonical enum never loses the source
   taxonomy.
3. **Three payload shapes**, formally defined and nested:
   `point_event ⊂ timed_span ⊂ generation_shaped`. A span is `generation_shaped`
   iff it carries any generation field; shape is decoupled from `kind` (any kind
   may carry generation fields — `raw_kind` preserves the semantics).
4. **Foreign-type mapping policy** is part of the spec ([`02-span.md`](../../api/model/v1alpha1/02-span.md)
   §2.2): every source type maps to exactly one `kind` with `raw_kind` set;
   unmapped types fall back to `span` and raise the `unmapped_kind` data-quality
   signal.
5. **The promoted set is fixed and small**; everything else lives in
   `attributes` with raw attributes always preserved. Adding a promoted field
   requires an ADR even though it is additive.

## Reasoning and evidence

- **Closed enum + `raw_kind`, not a free string.**
  > Evidence: Langfuse's observation `type` is a free-form `LowCardinality(String)`
  > whose authoritative 10-value enum lives in code and has already **diverged**
  > between its Postgres and ingestion definitions (study Ch. 06 §3). A free string
  > gives no storage-level guarantee and lets the taxonomy drift. A closed canonical
  > enum gives the Query API a stable dimension; `raw_kind` preserves fidelity and
  > round-tripping, so we lose nothing by closing the enum.

- **Seven kinds, mapping the observed taxonomies.** The seven cover the
  distinctions the studied dialects actually draw (LLM/embedding/tool/retrieval/
  agent/guardrail + generic), collapsing near-synonyms (Langfuse/OpenInference
  `CHAIN`, `EVALUATOR`, `RERANKER`) onto `agent_step`/`span` with `raw_kind` kept.
  The full mapping table is normative in [`02-span.md`](../../api/model/v1alpha1/02-span.md) §2.2.

- **Three shapes decoupled from kind.**
  > Evidence: Langfuse defines exactly this inheritance — EVENT (no end) ⊂ SPAN
  > (adds end) ⊂ GENERATION (adds model/usage/cost/prompt) — and wires all seven rich
  > subtypes to the generation body, so any subtype may carry usage/cost (study Ch.
  > 06 §3; Ch. 05 §2). We formalize the three shapes and decouple them from `kind`,
  > so a `tool_call` that reports token usage is representable without mislabeling
  > its kind as `generation`.

- **Promotion is permanent; keep the set small.**
  > Evidence: Langfuse retrofitted session/user indexes after table creation and its
  > aggregations only count rows where a promoted attribute was set at write time —
  > there is no retroactive backfill (study Ch. 08; digest §2). A promoted field is
  > therefore a near-permanent commitment. We fix a small promoted set and route the
  > long tail to `attributes`; additions require an ADR (LM-2).

## The promoted set

`trace_id, kind, name, start_time, end_time, status, environment, release,
version, session_id, user_id`, plus on `generation_shaped` spans the model and
usage/cost fields ([`06-usage-cost.md`](../../api/model/v1alpha1/06-usage-cost.md)).
Prompt linkage is a **reference** (`prompt_ref`), not a promoted column, because
prompts are a plugin concept (invariant 2); see [`02-span.md`](../../api/model/v1alpha1/02-span.md)
§5.4 and the ADR-0016 under-specification note.

## Consequences

- Stable, closed query dimensions; foreign taxonomies preserved, never lost.
- New source frameworks need only a mapping-table entry (a normalizer
  improvement), not a model change.
- Cost: the promoted set cannot grow casually; genuinely new first-class
  dimensions require an ADR and will not backfill history.
- `status` is OTel-tri-state (`unset/ok/error`); richer source severities
  (Langfuse `DEBUG`/`WARNING`) are demoted to `llmobs.raw.level` — an
  under-specification LM-2 left open, resolved here and flagged for review.

## Alternatives considered

- **Open/free-string `kind`.** Rejected — the study shows it drifts and gives no
  storage-level guarantee.
- **One flat span shape (all fields on every span).** Rejected — obscures the
  point-event/timed/generation distinction the dialects and the Query API rely on.
- **Promote prompt/model-resolution fields.** Rejected for prompt linkage (a
  plugin concept → reference). Model *name* (`model`) is promoted because it is a
  first-class generation query dimension; resolved model identity is a derived
  attribute, not promoted.

## Links

- Spec: [`02-span.md`](../../api/model/v1alpha1/02-span.md), [`span.schema.json`](../../api/model/v1alpha1/schema/span.schema.json).
- Parent: ADR-0016. Sibling: ADR-0017.
- Evidence: study Ch. 05, Ch. 06, Ch. 08; `findings-for-data-model.md` §1, §2.
