# Score and ScoreConfig (`v1alpha1`)

**Section type:** Normative except where marked *Informative*.

A **Score** is a named, typed **measurement** attached to a subject. It is the
kernel's evaluation primitive. A **ScoreConfig** is the optional definition that
constrains scores of a given name and type.

## 1. A score is a measurement only (Normative) — LM-3

`v1alpha1` scores are measurements. Free-text feedback and corrected model
outputs are **NOT** scores; they are a future plugin-owned `annotation` concept
(`01-entities.md` §4.3). Consequently the score value space is exactly three data
types (§2); there is no `TEXT` or `CORRECTION` score type.

> Evidence: Langfuse overloads its score record with five data types — adding TEXT
> and CORRECTION — then must exclude those two from every metric via
> `AGGREGATABLE_SCORE_TYPES` / `LISTABLE_SCORE_TYPES` allow-lists, and forces a
> CORRECTION's name to `"output"` and its numeric `value` to a sentinel `0` (study
> Ch. 07 §3, §4, §8). LM-3 rejects the overload: a score is a measurement; corrected
> outputs and free text are a different concept owned by a plugin. See ADR-0017.

### 1.1 Scores enter only through the score write path (Normative) — Q3

A **normalizer MUST NOT synthesize Score entities** from span attributes. A
normalizer cannot verify a score's `source` (§4) or its subject (§5), so
fabricating scores from, say, an `EVALUATOR` span or an OpenInference
`retrieval.documents.N.document.score` would invent provenance the model
guarantees. An evaluator's *execution* is a span; its *result* is a Score written
by whoever ran it, through the score write path, with a verified `source` and
subject (`02-span.md` §2.4).

**Per-document retrieval relevance is not a Score.** A retriever's per-document
scores are payload details of that retrieval (they live in the span's `output`),
not project-level measurements attached to a subject. They MUST NOT be promoted to
Score entities (design finding Q2).

## 2. Value model (Normative) — LM-3

A score has a `data_type` and **two** nullable value fields; `data_type` selects
which is authoritative. There are **no sentinel values**.

| `data_type` | `value_numeric` | `value_string` | Authoritative field |
|---|---|---|---|
| `numeric` | the number | MUST be null | `value_numeric` |
| `categorical` | the category's mapped number, or null if unmapped | the category label | `value_string` (with `value_numeric` as its optional mapped number) |
| `boolean` | `0` or `1` | MUST be null | `value_numeric` |

Rules:

- `data_type` ∈ `{numeric, categorical, boolean}` (closed enum).
- For `numeric`: `value_numeric` MUST be set; `value_string` MUST be null.
- For `boolean`: `value_numeric` MUST be `0` or `1`; `value_string` MUST be null.
- For `categorical`: `value_string` MUST be set (the label); `value_numeric` MAY
  carry the config-defined numeric mapping of that label, or be null when the
  score has no config or the label is unmapped. A `categorical` score MUST NOT be
  assigned a sentinel `0` to stand in for "no numeric value" — absence is `null`.

> Evidence: Langfuse stores a non-nullable `value Float64` plus a nullable
> `string_value`, forcing sentinel `0`s for non-numeric scores, and a separate
> `long_string_value` for CORRECTION text (study Ch. 07 §1, §4, §8). LM-3 makes both
> value fields nullable so absence is representable, and drops the long-text column
> with the CORRECTION type. See ADR-0017.

## 3. ScoreConfig — definition vs record split (Normative)

A **ScoreConfig** constrains scores of a given name/type. It is a distinct entity
from a Score. A Score MAY reference a ScoreConfig by id; the reference is
OPTIONAL.

| ScoreConfig field | Type | Null? | Semantics |
|---|---|---|---|
| `id` | string | no | Identity (`project_id, id`). |
| `project_id` | string | no | Tenant scope. |
| `name` | string | no | The score name this config governs. |
| `data_type` | enum `numeric`\|`categorical`\|`boolean` | no | Must match scores that reference it. |
| `is_archived` | boolean | no | Archived configs reject new scores; defaults `false`. |
| `min_value` | number | yes | `numeric` only: lower bound. |
| `max_value` | number | yes | `numeric` only: upper bound (MUST be `> min_value` when both set). |
| `categories` | array<{label, value}> | yes | `categorical`/`boolean` only: unique labels, unique values. |
| `description` | string | yes | Free text. |

Validation obligations when a Score references a ScoreConfig:

- The config's `data_type` MUST equal the score's `data_type`.
- The config's `name` MUST equal the score's `name` (a config-linked score's name
  is governed by the config).
- A `numeric` score's `value_numeric` MUST fall within `[min_value, max_value]`
  when those are set.
- A `categorical` score's `value_string` MUST be one of the config's category
  labels; its `value_numeric`, when set, MUST equal that category's `value`.
- A `boolean` config MUST define exactly two categories mapping to `0` and `1`.
- A Score referencing an `is_archived` config MUST be rejected.

A Score with no config MAY be ingested; its `data_type` is taken from the score
itself (or inferred: a number ⇒ `numeric`, a string ⇒ `categorical`).

> Evidence: Langfuse keeps `ScoreConfig` in Postgres (relational, authoritative) and
> score records in ClickHouse (columnar), validating record against config at
> ingestion then denormalizing; its config enum has four types and lacks a
> `(name, data_type)` uniqueness constraint (study Ch. 07 §2, §3, §4). This model
> keeps the definition/record split and the validation obligations, but aligns the
> config enum to the three measurement types.

## 4. Source (Normative) — LM-3

`source` records how a score was produced. It is a closed enum:

| `source` | Meaning |
|---|---|
| `human` | A person assigned the score (annotation). |
| `llm_judge` | An LLM-as-judge evaluator produced the score. |
| `code` | A deterministic/code evaluator produced the score. |
| `external` | Ingested from an external system via the public API. |

`source` MUST be one of these values. Unlike Langfuse, there is no
reserved-for-internal source: any producer sets the `source` that describes it.

> Evidence: Langfuse uses three sources (API/EVAL/ANNOTATION) and reserves EVAL so
> external callers cannot forge it (study Ch. 07 §3). LM-3 replaces this with four
> producer-descriptive sources, distinguishing `llm_judge` from `code` evaluation
> (which Langfuse's single EVAL conflates) and separating externally-ingested
> (`external`) from human (`human`).

## 5. Subjects — what a score attaches to (Normative) — LM-8

A score attaches to exactly one subject, expressed as a **`(subject_type,
subject_id)`** pair. This replaces a hardcoded set of nullable foreign-key
columns.

| Score field | Type | Null? | Semantics |
|---|---|---|---|
| `id` | string | no | Identity. |
| `project_id` | string | no | Tenant scope. |
| `subject_type` | string | no | §5.1 / §5.2. |
| `subject_id` | string | no | The referenced subject's id. |
| `name` | string | no | Score name (governed by config when linked). |
| `data_type` | enum | no | §2. |
| `value_numeric` | number | yes | §2. |
| `value_string` | string | yes | §2. |
| `source` | enum | no | §4. |
| `config_ref` | reference | yes | Optional `(type,id,label?)` to a ScoreConfig (`07-references.md`). |
| `comment` | string | yes | Free-text note attached to the measurement. |
| `metadata` | map<string,value> | no | Open map; defaults `{}`. |
| `timestamp` | timestamp | no | When the measurement was taken; identity/merge anchor. |
| `environment` | string | no | Sanitized dimension; frozen; defaults `"default"`. |

### 5.1 Kernel-defined subject types (Normative)

The kernel defines exactly these `subject_type` values:

| `subject_type` | `subject_id` refers to |
|---|---|
| `span` | A Span's `id` (the score MAY also, informally, concern the span's trace). |
| `trace` | A Trace's `id`. |
| `session` | A `session_id` value (a session is a dimension, not an entity — `01-entities.md` §4.1). |

### 5.2 Plugin-registered subject types (Normative)

A plugin MAY register additional subject types under its own **namespace**,
formatted `<plugin-namespace>/<name>` (for example `evals/dataset_run_item`). The
registration mechanism is abstract here: a plugin declares, through the plugin
registry (a kernel capability specified elsewhere), the namespaced subject types
it owns. Rules:

- A namespaced subject type MUST contain exactly one `/` separating a non-empty
  plugin namespace from a non-empty name.
- The kernel MUST NOT interpret the semantics of a plugin subject type; it stores
  and returns `(subject_type, subject_id)` verbatim and treats the reference as
  possibly-dangling (`07-references.md`).
- Kernel subject types (`span`, `trace`, `session`) MUST NOT contain `/` and are
  reserved.

> Evidence: Langfuse hardcodes four attachment points as nullable columns
> (`trace_id` + optional `observation_id`, `session_id`, `dataset_run_id`) enforced
> by a zod refine requiring exactly one, and had to add each new attachment as a
> schema migration (`0012` session, `0017` dataset run) with its own index (study
> Ch. 07 §1, §5). LM-8 generalizes to a `(subject_type, subject_id)` pair so new
> attachment points — especially plugin-owned ones like dataset run items — need no
> kernel schema change.

### 5.3 Dataset linkage is plugin guidance, not kernel spec (Informative)

Datasets, runs, and run items are owned by the evals plugin (`01-entities.md`
§4.2). A dataset-run-item score attaches via `subject_type = "evals/dataset_run_item"`.
The Langfuse reproducibility patterns are recommended plugin guidance:

> Evidence: Langfuse's `dataset_run_items_rmt` denormalizes point-in-time snapshots
> of the run and the dataset item (input/expected-output/metadata, plus the item's
> SCD-2 `validFrom` version) into every row, so an experiment result stays
> reproducible after the item is edited (study Ch. 03; Ch. 10; digest §8). A plugin
> implementing datasets SHOULD adopt snapshot-into-fact-row + versioned items for the
> same reason; the kernel does not mandate or store it.

## 6. Identity, merge, uniqueness (Normative)

- Score idempotency key: `(project_id, id)` (`01-entities.md` §3.2).
- Frozen fields: `id`, `project_id`, `subject_type`, `subject_id`, `timestamp`,
  `environment` (`05-update-semantics.md` §3).
- Logical uniqueness of a human annotation (one score per subject per name/config)
  is NOT a storage constraint in this model; a producer that wants
  overwrite-not-accumulate semantics MUST reuse a stable `id` for the same
  `(subject, name, config)` so the idempotency key collapses re-annotations.

> Evidence: Langfuse enforces annotation uniqueness in application code, not storage:
> its annotation router looks up an existing score by `(project, target, name/config,
> dataType)` and reuses that row's id (study Ch. 07 §6). This model keeps uniqueness a
> producer responsibility via stable ids, and — unlike Langfuse — its idempotency key
> is `(project_id, id)` alone, so a reused id reliably overwrites regardless of
> `timestamp` or `name` drift (`01-entities.md` §3.3).

## 7. Validation failure handling (Normative)

Config-validation failure MUST be handled **identically** regardless of ingestion
path (synchronous public API or asynchronous batch). A score that fails
validation MUST be rejected observably (surfaced as an error and counted via a
data-quality signal, `08-data-quality.md`); it MUST NOT be silently dropped.

> Evidence: In Langfuse the *same* config validation returns a 400 synchronously but
> is silently swallowed (logged and dropped) on the async batch path (study Ch. 07
> §4, §8). This model prohibits the divergence: one validation, one observable
> outcome.
