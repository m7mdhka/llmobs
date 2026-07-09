# ADR-0017: Score model

- **Status:** Accepted
- **Date:** 2026-07-09
- **Deciders:** m7mdhka (data model design session)
- **Parent:** ADR-0016

## Context

Scores are the kernel's evaluation primitive. The Langfuse teardown study
(chapters 07, and `findings-for-data-model.md` §3, §5) documented a score model
that had accreted, under production pressure, several properties its own authors
work around at read time. This ADR records the reasoning for decisions **LM-3**
(value model, sources, measurement-only) and **LM-8** (subjects), including the
patterns we explicitly reject. Normative spec: [`04-score.md`](../../api/model/v1alpha1/04-score.md).

## Decision

1. **A score is a measurement only.** `data_type ∈ {numeric, categorical,
   boolean}`. Free-text feedback and corrected model outputs are NOT scores; they
   are a future plugin-owned `annotation` concept.
2. **No sentinel values.** A score has two nullable value fields, `value_numeric`
   and `value_string`; `data_type` selects the authoritative one. Absence is
   `null`, never `0` or `""`.
3. **Sources are producer-descriptive:** `{human, llm_judge, code, external}` —
   distinguishing LLM-judge from code evaluation, and external ingestion from
   human annotation.
4. **Subjects are `(subject_type, subject_id)` pairs.** Kernel subject types:
   `span`, `trace`, `session`. Plugins register namespaced subject types (e.g.
   `evals/dataset_run_item`). Datasets/runs/run-items are owned by the evals
   plugin, not the kernel.
5. **Definition/record split retained.** A `ScoreConfig` (definition) is a
   distinct entity a Score may reference; validation obligations are normative.
6. **Uniform validation outcome.** Config-validation failure is handled
   identically on every ingestion path (no silent async drops).

## Explicit rejections (with evidence)

- **Reject: `Float64`-non-null value with sentinel `0`.**
  > Evidence: Langfuse stores `value Float64` NOT NULL, forcing sentinel `0`s for
  > TEXT/CORRECTION/unconfigured-categorical scores (study Ch. 07 §1, §4, §8). We
  > make both value fields nullable so "no numeric value" is representable as
  > `null`, which the schema enforces per `data_type` ([`score.schema.json`](../../api/model/v1alpha1/schema/score.schema.json)).

- **Reject: CORRECTION and TEXT as score data types.**
  > Evidence: Langfuse added CORRECTION and TEXT "score" types, then had to exclude
  > them from every metric via `AGGREGATABLE_SCORE_TYPES`/`LISTABLE_SCORE_TYPES`
  > allow-lists, force a CORRECTION's name to `"output"`, and carry a separate
  > `long_string_value` column (study Ch. 07 §3, §8). Corrected outputs and free
  > text are not measurements; overloading the score table with them pollutes every
  > aggregate. They become a plugin-owned `annotation` concept
  > ([`01-entities.md`](../../api/model/v1alpha1/01-entities.md) §4.3).

- **Reject: a hardcoded set of nullable attachment columns.**
  > Evidence: Langfuse hardcodes attachment as nullable columns (`trace_id` +
  > optional `observation_id`, `session_id`, `dataset_run_id`) governed by a zod
  > refine, and had to add each new attachment (`0012` session, `0017` dataset run)
  > as a schema migration with its own index (study Ch. 07 §1, §5). A
  > `(subject_type, subject_id)` pair generalizes this so new attachment points —
  > especially plugin-owned ones — need no kernel schema change (LM-8).

- **Reject: a reserved-for-internal source.**
  > Evidence: Langfuse reserves `EVAL` so external callers cannot forge it, but this
  > conflates LLM-judge and code evaluation under one value (study Ch. 07 §3). We use
  > four producer-descriptive sources instead; authorization of who may write which
  > source is an auth concern, not a modeling one.

## Consequences

- Scores stay clean: every stored score is a real measurement, so aggregates need
  no allow-list filtering.
- The plugin ecosystem gains an open attachment mechanism (namespaced subject
  types) without kernel churn.
- Cost: annotations/corrections must wait for the future `annotation` concept;
  until then, corrected-output capture is out of scope for `v1alpha1`.
- Cost: uniqueness of a human annotation (one-per-subject-per-config) is a
  producer responsibility (stable `id` reuse), not a storage constraint.

## Alternatives considered

- **Keep five data types (numeric/categorical/boolean/text/correction).**
  Rejected — inherits the allow-list-everywhere maintenance burden the study
  documented.
- **Polymorphic per-type score sub-tables.** Rejected — heavier and unnecessary;
  a single record with a `data_type` tag and two nullable value fields, enforced
  by schema `if/then`, is sufficient and storage-neutral.

## Links

- Spec: [`04-score.md`](../../api/model/v1alpha1/04-score.md), [`score.schema.json`](../../api/model/v1alpha1/schema/score.schema.json), [`score-config.schema.json`](../../api/model/v1alpha1/schema/score-config.schema.json).
- Parent: ADR-0016. Sibling: ADR-0018.
- Evidence: study Ch. 07; `findings-for-data-model.md` §3, §5.
