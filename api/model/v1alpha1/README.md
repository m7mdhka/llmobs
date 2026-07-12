# Canonical Data Model (`v1alpha1`)

The storage-neutral **logical** data model of LLMObs — the model every ingestion
normalizer maps *into*, the Query API reads *from*, and every plugin consumes. It is a
**contract**, authored before any code, and it contains no Go, no TypeScript, and no SQL
DDL. Maturity is `v1alpha1`: additive-only within a major; a breaking change requires a new
maturity version with an ADR and a deprecation window.

Its load-bearing promise is **storage neutrality**: two independent adapters — the lite
profile (Postgres-only) and the scale profile (ClickHouse) — MUST produce identical
Query-API-observable behavior for the same sequence of ingested events. Every MUST/MUST NOT
here describes *observable semantics only* and is asserted against both adapters by the
conformance suite (`tools/conformance`); the merge test vectors in §6 are the sharpest
instrument of that obligation, written as executable data.

This document is the single normative model spec. Its companions in this directory are the
machine-readable **`schema/`** (JSON Schemas, Draft 2020-12, + valid/invalid examples,
*Normative*) and **`validation/`** (per-dialect field-by-field mapping worksheets,
*Informative*).

## Contents

1. [Overview](#1-overview) — *Normative*
2. [Entities and identity](#2-entities-and-identity) — *Normative*
3. [Span](#3-span) — *Normative*
4. [Trace](#4-trace) — *Normative*
5. [Score and ScoreConfig](#5-score-and-scoreconfig) — *Normative*
6. [Update semantics, idempotency, and the merge algorithm](#6-update-semantics-idempotency-and-the-merge-algorithm) — *Normative*
7. [Usage and cost](#7-usage-and-cost) — *Normative*
8. [References](#8-references) — *Normative*
9. [Data-quality signals](#9-data-quality-signals) — *Normative*
10. [Adapter guidance](#10-adapter-guidance) — *Informative*

---

## 1. Overview

**Status:** `v1alpha1` (maturity per [`VERSIONING.md`](../../../VERSIONING.md); additive-only
within a major, breaking changes require an ADR + deprecation window).
**Section type:** this file is **Normative** except where a block is marked
*Informative*.

> This specification is the storage-neutral **logical** data model of LLMObs. It
> is the model every ingestion normalizer maps *into*, the Query API reads
> *from*, and every plugin consumes. It is a **contract**, authored before any
> code (invariant 5). It contains no Go, no TypeScript, no SQL DDL.

### 1. Scope (Normative)

This model defines the canonical logical entities **Trace**, **Span**, **Score**,
**ScoreConfig**, and **MediaReference**, their fields, identity and idempotency
rules, update/merge semantics, usage/cost representation, cross-reference
conventions, and data-quality conventions.

It deliberately does **not** define: sessions, users, or threads as entities
([Entities & identity](#2-entities-and-identity) §4); datasets, dataset runs, or run items (owned by the evals
plugin); physical storage layout (informative guidance only, [Adapter guidance](#10-adapter-guidance));
the wire/transport encodings (OTLP, compat endpoints) — those map *into* this
model; or the Query API grammar (a separate contract that reads this model).

#### 1.1 Storage neutrality and the conformance obligation (Normative)

The model is **logical**. Two independent adapters — the lite profile
(Postgres-only) and the scale profile (ClickHouse) — MUST produce **identical
Query-API-observable behavior** for the same sequence of ingested events. All
normative text in this specification describes *observable semantics only*. No
requirement here may be satisfied by, or depend on, a particular physical
representation.

Where this document states a rule (a MUST/MUST NOT), the conformance suite
(`tools/conformance`) will assert it against both adapters. The merge test
vectors in [Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm) are the sharpest instrument of this
obligation and are written as executable data.

### 2. Relationship to OpenTelemetry GenAI (Normative)

OTLP + the OpenTelemetry GenAI semantic conventions are LLMObs's canonical
ingestion format (invariant 6). This model is designed as the **normalized
target** of that format:

- A canonical **Span** corresponds to an OTel span; a canonical **Trace** is the
  root aggregation over spans sharing a `trace_id`.
- Canonical identity ([Entities & identity](#2-entities-and-identity) §3) reuses OTel `trace_id` / `span_id`.
- Every attribute a normalizer does not promote to a canonical field MUST be
  preserved in the span's `attributes` map (invariant 6, "raw attributes are
  always preserved"). No ingested attribute is ever dropped.

This model is **not** a re-export of OTel: it adds first-class GenAI concepts
(kinds, generation payloads, usage/cost, scores) that the OTel span shape leaves
in attributes, and it is transport-neutral (compat plugins that speak other
wire formats normalize into the same model).

> Evidence: Langfuse runs two wire formats (legacy JSON envelope + OTLP) into one
> storage model, lowering OTLP back into its internal event schema and preserving
> unrecognized attributes under `metadata.attributes`/`.resourceAttributes`/`.scope`
> (study Ch. 05 §5, §8 — `OtelIngestionProcessor.ts:1016`). This model takes the
> same "one model, many wire formats, raw always preserved" stance while choosing
> an OTel-shaped canonical form rather than a bespoke internal envelope.

### 3. Reading guide (Informative)

| File | Type | Contents |
|---|---|---|
| [Overview](#1-overview) | Normative | scope, versioning, OTel relationship, glossary |
| [Entities & identity](#2-entities-and-identity) | Normative | entity inventory, hierarchy, identity, non-entities |
| [Span](#3-span) | Normative | span fields, kinds, payload shapes, dimensions, status, media tokens |
| [Trace](#4-trace) | Normative | trace fields, trace↔span merge relationship, tags |
| [Score & ScoreConfig](#5-score-and-scoreconfig) | Normative | score value model, sources, configs, subjects, plugin subject registration |
| [Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm) | Normative | merge algorithm, frozen fields, idempotency, **test vectors** |
| [Usage & cost](#7-usage-and-cost) | Normative | usage/cost maps, well-known keys, derivation precedence, pricing snapshot |
| [References](#8-references) | Normative | cross-reference format, label snapshots, dangling tolerance |
| [Data-quality](#9-data-quality-signals) | Normative | the counter/flag conventions used across the spec |
| [Adapter guidance](#10-adapter-guidance) | **Informative** | physical layout guidance (lite vs scale) |
| `schema/` | Normative | JSON Schemas (Draft 2020-12) + valid/invalid examples |
| `validation/` | Informative | per-dialect field-by-field mapping worksheets |

### 4. RFC 2119 keywords (Normative)

The key words **MUST**, **MUST NOT**, **REQUIRED**, **SHALL**, **SHALL NOT**,
**SHOULD**, **SHOULD NOT**, **RECOMMENDED**, **MAY**, and **OPTIONAL** are to be
interpreted as described in RFC 2119 and RFC 8174 when, and only when, they
appear in all capitals.

### 5. Glossary (Normative)

| Term | Definition |
|---|---|
| **Entity** | A canonical object with an identity: Trace, Span, Score, ScoreConfig, MediaReference. |
| **Promoted field** | A first-class, typed, queryable field on an entity (as opposed to a key in the `attributes` map). The promoted set is fixed per major version; additions require an ADR (LM-2). |
| **Attributes map** | The open key/value map holding every non-promoted attribute, including all raw ingested attributes. |
| **Kind** | The closed canonical enum classifying a span's semantic role ([Span](#3-span) §2). |
| **Payload shape** | One of three nested field profiles a span may take: `point_event ⊂ timed_span ⊂ generation_shaped` ([Span](#3-span) §3). |
| **Dimension** | A promoted grouping field: `environment`, `release`, `version`, `session_id`, `user_id`. |
| **Provided vs resolved** | For usage/cost: `provided_*` is what the client sent; the resolved value is what kernel enrichment computed ([Usage & cost](#7-usage-and-cost)). |
| **`event_ts`** | The logical version stamp used to order updates to an entity; latest wins per field-group ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm)). |
| **Tombstone** | A logical delete marker (`is_deleted`) applied by re-inserting the entity's key with a later `event_ts`. |
| **Frozen field** | A field immutable after first write; a later value is rejected/normalized, never applied ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm) §3). |
| **Subject** | The `(subject_type, subject_id)` target a score attaches to ([Score & ScoreConfig](#5-score-and-scoreconfig) §5). |
| **Reference** | A `(type, id)` pointer to another entity or plugin-owned object, optionally carrying a label snapshot ([References](#8-references)). |
| **Data-quality signal** | A counter or boolean flag the model raises when it normalizes, coerces, or truncates input rather than failing ([Data-quality](#9-data-quality-signals)). |

### 6. Versioning statement (Normative)

This is `v1alpha1`. Within a maturity version, changes MUST be additive
(new OPTIONAL fields, new kinds, new well-known map keys). Removing, renaming,
retyping, or tightening an existing field is breaking and MUST proceed via a new
maturity version (`v1beta1`, then `v1`) with an ADR and a deprecation window.
Adding a field to the **promoted set** ([Span](#3-span) §1) is a model change that
requires an ADR even though it is additive, because promotion is a near-permanent
commitment ([Span](#3-span) §1.1).

---

## 2. Entities and identity

**Section type:** Normative.

### 1. Entity inventory (Normative)

The canonical model defines exactly these entities:

| Entity | Identity | Owned by | Summary |
|---|---|---|---|
| **Trace** | `(project_id, id)` | kernel | The root of one end-to-end unit of work; an aggregation over its spans. |
| **Span** | `(project_id, id)` | kernel | One unit of work inside a trace: a `point_event`, a `timed_span`, or a `generation_shaped` operation ([Span](#3-span)). |
| **Score** | `(project_id, id)` | kernel | A named, typed **measurement** attached to a subject ([Score & ScoreConfig](#5-score-and-scoreconfig)). |
| **ScoreConfig** | `(project_id, id)` | kernel | The definition constraining scores of a given name/type ([Score & ScoreConfig](#5-score-and-scoreconfig) §3). |
| **MediaReference** | `(project_id, sha256)` | kernel | A content-addressed pointer to an out-of-band binary payload ([Span](#3-span) §7). |

Tenancy is a two-level hierarchy above the model: **Organization → Project**.
Organization and Project are control-plane concepts owned by the kernel's
auth/tenancy subsystem, not data-model entities; every entity above is
**project-scoped** (carries `project_id`). No entity carries an organization
identifier — organization scoping MUST be resolved before a Query API call
reaches the data model.

> Evidence: Langfuse resolves org scoping entirely in Postgres/API before any
> analytical query; `project_id` is the only tenant key the analytical store sees,
> and it leads every physical sort key (study Ch. 14 §1; Ch. 03). This model keeps
> the same "project is the data-plane tenant boundary; org lives above" split.

### 2. Hierarchy (Normative)

```
Organization            (control plane, not an entity)
└── Project             (control plane, not an entity; project_id scopes all below)
    └── Trace
        └── Span            (parent_span_id forms an in-trace tree; a span MAY be a point event)
            └── SpanEvent   (nested (name, timestamp, attributes) records; NOT an entity — [Span](#3-span) §4.4)
    └── Score               (attaches to a subject: a span, a trace, a session, or a plugin type)
    └── ScoreConfig         (definition; a Score MAY reference one)
    └── MediaReference      (referenced from span input/output/attributes via a token)
```

- A **Span** belongs to exactly one Trace (`trace_id`) and MAY nest under another
  span in the same trace via `parent_span_id`. The tree is reconstructed from
  parent pointers; the model stores **no** root/depth/path field.
- A **Score** attaches to a subject via `(subject_type, subject_id)` ([Score & ScoreConfig](#5-score-and-scoreconfig)
  §5). A score is not part of the span tree.

> Evidence: Langfuse observations nest via `parent_observation_id` with no stored
> depth/path, reconstructed at read time (study Ch. 06 §4). This model adopts the
> same parent-pointer tree.

### 3. Identity and idempotency (Normative) — LM-6

#### 3.1 Identifiers

- **`Trace.id`** MUST be the OTel `trace_id` when the span data originates from
  OTLP, rendered as a lowercase hex string (32 hex chars for a 16-byte id).
- **`Span.id`** MUST be the OTel `span_id` when present, rendered as a lowercase
  hex string (16 hex chars for an 8-byte id). `Span.parent_span_id`, when set,
  MUST be the parent OTel `span_id` rendered the same way.
- When an id does not originate from OTel (e.g. a compat plugin or a
  client-supplied id), the normalizer MUST derive a stable id by a deterministic
  rule declared by that normalizer (for example, a namespaced hash of the source
  id). Derivation MUST be deterministic: the same source event MUST always yield
  the same canonical id, so re-delivery is idempotent.
- **`Score.id`**, **`ScoreConfig.id`**: kernel- or client-supplied stable string
  ids, unique within `(project_id, entity)`.

#### 3.2 The idempotency key

The idempotency key of a Trace, Span, or Score is **`(project_id, id)`**. Two
ingested events bearing the same `(project_id, id)` for the same entity type MUST
be treated as **the same entity** — the second is an update to the first
([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm)), never a new row observable through the Query API.

Idempotency MUST NOT depend on any field other than `(project_id, id)` — in
particular, it MUST NOT depend on `start_time` or `name`. (This is a deliberate
divergence from Langfuse; see §3.3.)

#### 3.3 Divergence from Langfuse (Informative)

> Evidence: In Langfuse the *effective* dedup identity is the full storage sort
> key — e.g. `(project_id, toDate(timestamp), name, id)` for scores — so reusing an
> `id` across a UTC-day boundary or changing `name` produces a **second** physical
> row that only `LIMIT 1 BY id` reads resolve, and a 5s/15s queue delay exists
> specifically to dodge the day-boundary hazard (study Ch. 03; Ch. 07 §6; digest
> §6). LM-6 rejects this: the canonical idempotency key is `(project_id, id)` alone,
> `start_time` is frozen ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm) §3) so it cannot move an entity
> across a partition, and there are **no** date-boundary delay mechanisms. Adapters
> MUST NOT let a physical sort key leak into idempotency; the scale adapter's
> read-time dedup MUST key on `(project_id, id)` ([Adapter guidance](#10-adapter-guidance)).

### 4. What is deliberately NOT an entity (Normative)

The following are intentionally absent from the canonical model. A normalizer or
plugin MUST NOT expect the kernel to store them as first-class objects.

#### 4.1 Sessions, users, threads — LM-7

`session_id` and `user_id` are **promoted free-form string dimensions** on spans
and traces ([Span](#3-span) §4), not entities. There is **no** Session, User, or
Thread entity. Consequences:

- Session- and user-level views are **derived by grouping** on the dimension at
  query time; the model defines no session/user record and no per-session
  aggregate. (Acceleration such as a materialized session index is an adapter
  concern, invisible to the logical model — [Adapter guidance](#10-adapter-guidance).)
- A session MAY span multiple users; the model imposes **no** one-user-per-session
  constraint.
- Multi-turn conversations are represented as multiple traces sharing a
  `session_id`, ordered by `start_time`. There is no Thread abstraction.
- UI/session flags (bookmark, public, etc.) are **not** in this model; they are
  plugin `kv` state.

> Evidence: Langfuse models sessions/users as derived-by-grouping over free-form
> promoted trace columns with no source-of-truth entity; its one Postgres session
> row is a thin lazily-created UI-flag side-table, and multi-turn = N traces sharing
> `session_id`; multi-user sessions are permitted (study Ch. 08; digest §7). This
> model adopts that stance and pushes UI flags out to plugins.

#### 4.2 Datasets, dataset runs, run items — LM-8

Datasets, dataset runs, and run items are **not** kernel entities; they belong to
the **evals plugin**. A Score MAY attach to a plugin-registered subject type such
as `evals/dataset_run_item` ([Score & ScoreConfig](#5-score-and-scoreconfig) §5.2), but the canonical model stores no
dataset structure. The Langfuse dataset patterns (SCD-2 versioned items,
snapshot-into-fact-row for reproducibility) are referenced as **recommended
plugin guidance** in [Score & ScoreConfig](#5-score-and-scoreconfig) §5.3, not kernel specification.

> Evidence: Langfuse keeps dataset *definitions* in Postgres and *run items* in
> ClickHouse with denormalized point-in-time snapshots so results stay reproducible
> when an item is later edited (study Ch. 10; Ch. 03; digest §8). LM-8 moves this
> whole surface into a plugin and keeps only the score↔subject linkage in the
> kernel.

#### 4.3 Annotations, corrections, free-text feedback — LM-3

Free-text feedback and corrected model outputs are **not scores** and are **not**
in `v1alpha1`. They are a future `annotation` concept owned by plugins. A Score
is a measurement only ([Score & ScoreConfig](#5-score-and-scoreconfig) §2).

> Evidence: Langfuse overloads its score table with CORRECTION and TEXT "scores"
> that it must then exclude from every metric via `AGGREGATABLE_SCORE_TYPES` /
> `LISTABLE_SCORE_TYPES` allow-lists (study Ch. 07 §3, §8). LM-3 refuses this
> overload: measurements are scores; corrected outputs/feedback are a separate
> plugin-owned concept. See ADR-0017.

---

## 3. Span

**Section type:** Normative except where marked *Informative*.

A **Span** is one unit of work inside a trace. It is the workhorse entity: an LLM
generation, a tool call, a retrieval, an agent step, a guardrail check, a generic
span, or a point-in-time event are all spans, distinguished by `kind` (§2) and
constrained by a **payload shape** (§3).

### 1. The promoted field set (Normative) — LM-2

The **promoted set** is the closed list of first-class, typed, queryable span
fields. It is exactly:

`trace_id, kind, name, start_time, end_time, status, environment, release,
version, session_id, user_id`

plus, on `generation_shaped` spans only, the `model`, `provider`, and usage/cost
fields defined in §5 and [Usage & cost](#7-usage-and-cost). (`provider` was added to the promoted
set by design finding F7; see ADR-0018 addendum.)

Every other attribute — no matter how it arrived — MUST live in the `attributes`
map (§6). Raw ingested attributes are ALWAYS preserved there (invariant 6).

#### 1.1 Promotion is a near-permanent commitment (Normative)

Adding a field to the promoted set is an additive change that nonetheless
REQUIRES an ADR ([Overview](#1-overview) §6). Rationale, carried from the study:

> Evidence: Langfuse retrofitted `session_id`/`user_id` indexes after table
> creation (`0005`, `0006`), and its aggregation queries only count rows where a
> promoted attribute was set at write time — there is **no** retroactive backfill
> (study Ch. 08; digest §2). A promoted field therefore behaves as a semi-permanent
> commitment: adding one later does not repair historical rows. The promoted set is
> deliberately small; the long tail lives in `attributes`.

### 2. Kind (Normative) — LM-1

`kind` is a **closed canonical enum**. `v1alpha1` defines exactly:

| `kind` | Meaning | Default shape (§3) |
|---|---|---|
| `span` | A generic unit of work with no LLM-specific semantics. | `timed_span` |
| `generation` | An LLM inference call (chat/completion/response). | `generation_shaped` |
| `embedding` | An embedding-model call. | `generation_shaped` |
| `tool_call` | Execution of a tool/function. | `timed_span` |
| `retrieval` | A retrieval/search step (e.g. vector search). | `timed_span` |
| `agent_step` | One step of an agent's control loop. | `timed_span` |
| `guardrail` | A guardrail / safety / policy check. | `timed_span` |

- `kind` is **frozen** after first write ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm) §3).
- New kinds are **additive-only** within a major ([Overview](#1-overview) §6); a
  consumer encountering an unknown kind MUST treat it as `span` (forward
  compatibility) rather than error.
- A span of any kind MAY be recorded as a `point_event` (no `end_time`) when it
  represents an instantaneous occurrence (§3.1).

#### 2.1 `raw_kind` (Normative)

The span MUST carry `raw_kind`: the original, pre-normalization type string from
the source dialect (e.g. Langfuse `CHAIN`, OpenInference `LLM`, OTel GenAI
`execute_tool`), or `null` when the source had no distinct type. `raw_kind` is
informational and preserved for round-tripping and audit; it is never
interpreted by the Query API.

> Evidence: Langfuse's observation `type` is a free-form string with a
> code-authoritative 10-value enum that has already diverged between its Postgres
> and ingestion definitions (study Ch. 06 §3). LM-1 closes the canonical enum to
> seven kinds for storage-level guarantees while preserving the original in
> `raw_kind`, so foreign taxonomies are never silently lost nor allowed to expand
> the canonical surface. See ADR-0018.

#### 2.2 Foreign-type mapping policy (Normative)

A normalizer MUST map every source type onto exactly one canonical `kind` and set
`raw_kind` to the source value. The mapping for the taxonomies known at
`v1alpha1`:

| Source type | Canonical `kind` |
|---|---|
| OTel GenAI `chat`, `text_completion`, `generate_content` | `generation` |
| OTel GenAI `embeddings` | `embedding` |
| OTel GenAI `execute_tool` | `tool_call` |
| OTel GenAI `invoke_agent`, `create_agent` | `agent_step` |
| OpenInference `LLM` | `generation` |
| OpenInference `EMBEDDING` | `embedding` |
| OpenInference `TOOL` | `tool_call` |
| OpenInference `RETRIEVER` | `retrieval` |
| OpenInference `AGENT` | `agent_step` |
| OpenInference `GUARDRAIL` | `guardrail` |
| OpenInference `CHAIN` | `span` (§2.3) |
| OpenInference `RERANKER`, `EVALUATOR` | `span` (with `raw_kind` preserved; §2.4) |
| Langfuse `GENERATION` | `generation` |
| Langfuse `EMBEDDING` | `embedding` |
| Langfuse `TOOL` | `tool_call` |
| Langfuse `RETRIEVER` | `retrieval` |
| Langfuse `AGENT` | `agent_step` |
| Langfuse `GUARDRAIL` | `guardrail` |
| Langfuse `CHAIN` | `span` (§2.3) |
| Langfuse `EVALUATOR` | `span` (§2.4) |
| Langfuse `SPAN` | `span` |
| Langfuse `EVENT` | `span` recorded as a `point_event` (§3.1) |

A source type with no entry MUST map to `span` with `raw_kind` set. Adding an
entry to this table is additive (a normalizer improvement), not a model change.

**The mapping MUST be a pure function of the span's own data.** A normalizer is a
pure per-span function; it MUST NOT base the `kind` decision on other spans (e.g.
whether children exist), because those may not have arrived at normalize time and
`kind` is frozen ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm) §3) — a stateful guess would raise a
`frozen_field_conflict` for a purely structural reason.

#### 2.3 `CHAIN` maps to `span` (Normative)

`CHAIN` (OpenInference, Langfuse) MUST map to `kind = span`, never `agent_step`.
"Does it orchestrate sub-steps" is not evaluable from the span alone, and a
speculative `agent_step` would pollute agent analytics with every trivial
sequence/`RunnableSequence`. `raw_kind = "chain"` keeps chains queryable
(via reference/attribute filtering, [References](#8-references)) without expanding the
frozen enum. This resolves the ambiguity LM-1 originally left open (design
finding F1).

#### 2.4 Evaluators map to `span`; normalizers MUST NOT synthesize scores (Normative)

`EVALUATOR`/`RERANKER` map to `kind = span` with `raw_kind` preserved. An
evaluator's *execution* is a span (it has latency, cost, and its own child LLM
calls); its *result* is a **Score** emitted through the score write path by
whoever ran it, with verified `source` and subject ([Score & ScoreConfig](#5-score-and-scoreconfig)).

A normalizer MUST NOT synthesize Score entities from span attributes. A normalizer
cannot verify a score's `source` (human vs llm_judge vs code) or its subject, so
inventing scores from spans would fabricate provenance. Scores enter only through
the score write path (design finding Q3).

### 3. Payload shapes (Normative) — LM-1

The model defines **three** nested payload shapes. Each is a strict superset of
the previous. A span's `kind` declares its **default** shape (§2); a span MAY be
recorded at a *narrower* shape than its kind's default (e.g. a `generation`
recorded as a `point_event`) but MUST NOT be recorded at a *wider* shape than
`generation_shaped`.

```
point_event  ⊂  timed_span  ⊂  generation_shaped
```

#### 3.1 `point_event` (Normative)

An instantaneous occurrence. Fields: the full promoted set **except** `end_time`
and the generation fields. `end_time` MUST be absent (or equal to `start_time`).
A `point_event` MUST NOT carry any generation field (§5); if one is present the
span is not a `point_event`.

#### 3.2 `timed_span` (Normative)

A `point_event` plus `end_time` (a `timed_span` MAY still be open — `end_time`
absent — until a later update closes it). A `timed_span` MUST NOT carry
generation fields.

#### 3.3 `generation_shaped` (Normative)

A `timed_span` plus the generation fields (§5): `model`, `provider`,
`model_parameters`, `completion_start_time`, the usage/cost fields
([Usage & cost](#7-usage-and-cost)), and the OPTIONAL `prompt_ref` (§5.4). A span MUST be
`generation_shaped` if and only if it carries any generation field. Only `generation` and `embedding` kinds default
to this shape, but any kind MAY carry generation fields if the instrumentation
does (`raw_kind` preserves the original semantics).

> Evidence: Langfuse defines exactly this inheritance — `EVENT` (no `endTime`) ⊂
> `SPAN` (adds `endTime`) ⊂ `GENERATION` (adds model/usage/cost/prompt) — and wires
> all seven "rich" subtypes (AGENT/TOOL/CHAIN/…) to the generation body so any
> subtype may carry usage/cost (study Ch. 06 §3; Ch. 05 §2). LM-1 formalizes the
> three shapes and decouples them from `kind`, so a `tool_call` that reports token
> usage is representable without forcing its kind to `generation`.

### 4. Field reference — promoted fields (Normative)

Types below are logical. `timestamp` is an instant with millisecond precision or
finer, serialized as RFC 3339 UTC. `string` is UTF-8.

| Field | Type | Null? | Shape | Frozen? | Semantics |
|---|---|---|---|---|---|
| `id` | string | no | all | **yes** | Span identity; OTel `span_id` hex where present ([Entities & identity](#2-entities-and-identity) §3). |
| `project_id` | string | no | all | **yes** | Tenant scope. |
| `trace_id` | string | no | all | **yes** | Owning trace ([Trace](#4-trace)). |
| `parent_span_id` | string | yes | all | no | In-trace parent; `null` for a trace-root span. |
| `kind` | enum | no | all | **yes** | §2. |
| `raw_kind` | string | yes | all | no | §2.1. |
| `name` | string | no | all | no | Human-readable operation name. |
| `start_time` | timestamp | no | all | **yes** | Span start; also the merge/identity anchor ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm)). |
| `end_time` | timestamp | yes | `timed_span`+ | no | Span end; absent ⇒ open or `point_event`. |
| `status` | object | no | all | no | §4.1. Defaults to `{code: "unset"}`. |
| `environment` | string | no | all | **yes** | §4.2. Defaults to `"default"`. |
| `release` | string | yes | all | no | §4.2. |
| `version` | string | yes | all | no | §4.2. |
| `session_id` | string | yes | all | no | Dimension ([Entities & identity](#2-entities-and-identity) §4.1). |
| `user_id` | string | yes | all | no | Dimension. |
| `input` | opaque | yes | all | no | §4.3. |
| `output` | opaque | yes | all | no | §4.3. |
| `input_content_type` | string | yes | all | no | §4.3. MIME-style rendering hint; NOT a queryable/promoted field. |
| `output_content_type` | string | yes | all | no | §4.3. Rendering hint; not promoted. |
| `events` | array<SpanEvent> | no | all | no | §4.4. Defaults to `[]`. Union-merged. |
| `attributes` | map<string, value> | no | all | no | §6. Defaults to `{}`. |

Generation fields (§5) are added on `generation_shaped` spans only. `events`,
`input_content_type`, and `output_content_type` are present on all shapes but are
NOT part of the queryable promoted set (§1); they carry structured trace detail
and rendering hints, not query dimensions.

#### 4.1 Status model (Normative)

`status` is OTel-aligned: `{ code: "unset" | "ok" | "error", message?: string }`.

- `code` defaults to `"unset"`. **`status.code` is `"error"` if and only if the
  source signals an error** (design finding F4): an OTel `status.code = ERROR`, a
  source severity/level of `ERROR`, or an exception event. Nothing else sets
  `"error"`.
- **A finish reason is not an error.** `gen_ai.response.finish_reasons` values
  such as `stop`, `length`, or `content_filter` MUST NOT set `status.code =
  "error"`; they are preserved in `attributes` (a consumer may treat
  `content_filter`/`length` as degraded, but the model does not). Similarly, an
  `error.type` attribute is a **well-known attribute** (§6.1), preserved in
  `attributes`; it does not by itself force `status.code = "error"` (only a real
  error signal does).
- `message` is free text (e.g. the OTel status message or an exception summary).
- **Severity beyond error** (e.g. Langfuse `DEBUG`/`WARNING`, syslog levels) is
  NOT promoted. A normalizer that receives a richer severity MUST map it onto
  `status.code` (only an error-equivalent sets `"error"`) and preserve the
  original severity in `attributes` under the reserved key `llmobs.raw.level`
  ([Data-quality](#9-data-quality-signals) §4), which keeps DEBUG/WARNING filterable.

> Under-specification resolved (flagged for review): LM-2 promotes `status` but does
> not define its shape or the fate of Langfuse's four-value `level`
> (DEBUG/DEFAULT/WARNING/ERROR, study Ch. 06 §3). This spec chooses an OTel
> tri-state status (`unset|ok|error`) as canonical and demotes non-error severity to
> a preserved attribute. See the final report's "under-specified" list.

#### 4.2 Dimensions: environment, release, version (Normative) — LM-11

- **`environment`** — a sanitized, low-cardinality, project-scoped string;
  defaults to `"default"`; **frozen** after first write. Sanitization
  (lowercase, reserved-prefix strip, length cap, charset) is defined once in
  [Data-quality](#9-data-quality-signals) §2 and MUST be executed in the `normalize` middleware
  stage for **every** transport, including compat plugins. Coercion to `"default"`
  MUST NOT happen silently: the offered value MUST be preserved in `attributes`
  under `llmobs.raw.environment` and the data-quality counter incremented
  ([Data-quality](#9-data-quality-signals)).
- **`release`** — build/deployment identity (e.g. a git SHA or build tag).
  Trace-level in meaning; on a span it mirrors the trace's release. MAY be null.
- **`version`** — per-call logic/prompt version; MAY differ per span. Present on
  both traces and spans.

> Evidence: Langfuse promotes `environment` (LowCardinality, `DEFAULT 'default'`,
> project-scoped, sanitized: lowercase / reserved-prefix strip / 40-char cap /
> silent `.catch()`→"default"), `release` (per-SDK-client build identity,
> trace-level), and `version` (per-call, on traces and observations) (study Ch. 08;
> Ch. 05 §8). Critically, Langfuse's OTLP path does **not** run the same
> normalization as its legacy path, so the same value can be stored with different
> casing/length by transport (study Ch. 05 §8, "environment normalization differs by
> transport"). LM-11 fixes this by mandating one sanitization routine in the shared
> `normalize` stage for all transports, and by making silent coercion observable.

#### 4.3 `input` / `output` (Normative) — LM-10

`input` and `output` are **opaque** payloads (typically serialized JSON, but the
model does not require any structure). The kernel MUST NOT perform recursive
schema validation of their contents. They MAY contain media reference tokens
(§7). An adapter MAY truncate an oversized payload only under the rules of §7.2.

**Content-type hints (F6).** `input_content_type` and `output_content_type` are
OPTIONAL MIME-style strings (e.g. `application/json`, `text/plain`) describing how
to render the corresponding payload. They are **rendering hints only** — NOT
queryable/promoted, not interpreted by the kernel, and never used for filtering.
A normalizer that receives a content type (e.g. OpenInference `input.mime_type`)
SHOULD set them.

**Composition precedence (Q1).** When a source encodes the same message/turn
content in **both** flattened attribute arrays (e.g. `gen_ai.prompt.N.*`,
`llm.input_messages.N.*`) **and** span events (§4.4), the flattened attribute
arrays are **authoritative** for composing `input`/`output`; the event-encoded
records are additionally preserved as span events (§4.4). Neither encoding is
dropped.

> Evidence: Langfuse deliberately skips recursive JSON validation of `input`/`output`
> for CPU cost and passes them through opaque (study Ch. 04 §10; digest §10). This
> model adopts the opaque-passthrough rule.

#### 4.4 Span events (Normative) — Q1

A span MAY carry **span events**: an ordered list of generic `(name, timestamp,
attributes)` records attached to the span. This is exactly OpenTelemetry's native
span-event shape. `v1alpha1` deliberately does **not** define a typed
message/chat model — that is deferred to a later maturity version after real
usage (design finding Q1) — so a span event is generic:

| SpanEvent field | Type | Null? | Semantics |
|---|---|---|---|
| `name` | string | no | Event name (e.g. `gen_ai.user.message`, `exception`, `gen_ai.choice`). |
| `timestamp` | timestamp | no | When the event occurred (preserves per-event timing that opaque `input`/`output` would lose). |
| `attributes` | map<string, value> | no | The event's own attributes; defaults `{}`. Raw attributes preserved (invariant 6). |

- `events` defaults to `[]` and is present on all payload shapes (a `point_event`
  MAY carry events).
- Span events are **not** entities and have no identity of their own; they are a
  nested structure of their span.
- A normalizer MUST represent OTel span events (timestamped log-records on a span,
  e.g. `gen_ai.system.message`, `gen_ai.choice`, `exception`) as span events,
  preserving each event's `name`, `timestamp`, and `attributes`, rather than
  collapsing them into `attributes` and discarding their timing.
- `events` is **union-merged** on update ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm) §3.1): a later
  event set adds events; identical events (same `name`, `timestamp`, and
  `attributes`) are deduplicated, so re-delivery is idempotent.

> Rationale: OTel span events carry their own timestamps and ordering that opaque
> `input`/`output` cannot preserve, and every framework models chat messages
> differently (design finding Q1 across OTel GenAI, OpenInference, OpenLLMetry). A
> generic `(name, timestamp, attributes)` record captures the native OTel shape
> without committing the model to any one message schema.

### 5. Generation fields (Normative)

Present only on `generation_shaped` spans (§3.3). Detailed usage/cost semantics
are in [Usage & cost](#7-usage-and-cost); this section fixes the fields.

| Field | Type | Null? | Semantics |
|---|---|---|---|
| `model` | string | yes | The **resolved/served** model — the model that actually ran (e.g. OTel `gen_ai.response.model`, `gpt-4o-2024-08-06`). The requested model (e.g. `gen_ai.request.model`, `gpt-4o`), when it differs, is preserved under `llmobs.raw.model_requested`. (F7) |
| `provider` | string | yes | The model provider (e.g. `openai`, `anthropic`, `bedrock`). Promoted so cost derivation and analytics have `(provider, model)` as typed fields, not attribute lookups. (F7) |
| `model_parameters` | map<string, string> | yes | Request parameters as **verbatim strings**, keyed by name. The kernel does **not** parse them at normalize time (opaque rule, F2). Well-known keys: `temperature`, `max_tokens`, `top_p`, `top_k`, `frequency_penalty`, `presence_penalty`, `stop_sequences`, `seed`. Any additional key is permitted. |
| `completion_start_time` | timestamp | yes | Time to first token, for streaming generations. |
| `provided_usage_details` | map<string, integer≥0> | no | Usage as sent by the client. Defaults to `{}`. ([Usage & cost](#7-usage-and-cost) §2) |
| `usage_details` | map<string, integer≥0> | no | Resolved usage. Defaults to `{}`. ([Usage & cost](#7-usage-and-cost) §3) |
| `provided_cost_details` | map<string, decimal> | no | Cost as sent by the client. Defaults to `{}`. ([Usage & cost](#7-usage-and-cost) §2) |
| `cost_details` | map<string, decimal> | no | Resolved cost. Defaults to `{}`. ([Usage & cost](#7-usage-and-cost) §3) |
| `total_cost` | decimal | yes | Scalar total cost ([Usage & cost](#7-usage-and-cost) §4). |
| `cost_source` | enum `provided`\|`derived` | yes | How `cost_details` was obtained ([Usage & cost](#7-usage-and-cost) §4). |
| `pricing_snapshot_ref` | reference | yes | Reference to the price entry used for derivation ([Usage & cost](#7-usage-and-cost) §5, [References](#8-references)). |
| `prompt_ref` | reference | yes | §5.4. |

#### 5.4 `prompt_ref` (Normative)

`prompt_ref` is an OPTIONAL `(type, id, label?)` reference ([References](#8-references)) to
a prompt owned by the prompt-management plugin. It is **not** a promoted,
queryable scalar and does **not** expand the promoted set; prompts are a plugin
concept (invariant 2). A normalizer that receives prompt linkage (e.g. Langfuse
`prompt_name`/`prompt_version`) MUST record it as a `prompt_ref` with a label
snapshot rather than as promoted columns.

> Under-specification resolved (flagged): LM-1 lists "prompt-linkage" as part of the
> `generation_shaped` shape, but LM-2's promoted set omits prompt fields. This spec
> resolves the tension by representing prompt linkage as a **reference** (LM-12),
> keeping prompts a plugin concern (D2). See the final report.

### 6. The attributes map (Normative)

`attributes` is an open `map<string, value>` where `value` is a JSON scalar,
array, or object. It holds every non-promoted attribute. Rules:

- A normalizer MUST place every source attribute it does not map to a promoted
  field into `attributes`, unchanged, so **no ingested attribute is ever lost**
  (invariant 6). This includes OTel resource and scope attributes.
- Keys under the reserved namespace **`llmobs.*`** are owned by the kernel and
  carry canonical-preserved values (`llmobs.raw.*`, data-quality signals
  `llmobs.dq.*` — [Data-quality](#9-data-quality-signals)). Normalizers and plugins MUST NOT write
  arbitrary keys under `llmobs.*`.
- The map is deep-merged on update ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm) §2).

> Evidence: Langfuse preserves unrecognized OTLP span/resource/scope attributes under
> `metadata.attributes` / `.resourceAttributes` / `.scope` (study Ch. 05 §5). This
> model requires the same total preservation but under a single `attributes` map with
> a reserved `llmobs.*` namespace for kernel-owned keys.

#### 6.1 Well-known attribute conventions (Normative) — Q2, F4

Some cross-span relationships that are not promoted fields are carried by
**well-known attribute keys**. These are documented conventions, not schema — a
consumer that understands them gets extra power; one that does not still sees
plain attributes.

| Well-known key | Meaning |
|---|---|
| `llmobs.call_id` | Correlates a generation's emitted tool call to the `tool_call` span that executed it. A `generation` span sets `llmobs.call_id` (or one per parallel call) to the provider tool-call id; the corresponding `tool_call` span sets the same `llmobs.call_id`. This gives queryable generation↔tool-call linkage without a typed field (design finding Q2). |
| `error.type` | The error class/type when a span failed (e.g. `TimeoutError`). Preserved verbatim; does not by itself set `status.code = "error"` (§4.1, F4). |
| `llmobs.raw.*`, `llmobs.dq.*` | Kernel-reserved ([Data-quality](#9-data-quality-signals)). |

Per-document retrieval relevance (e.g. `retrieval.documents.N.document.score`)
stays in the `output` payload; those are payload details of a retrieval, not
project-level measurements, so they are **not** Score entities (design finding
Q2, see [Score & ScoreConfig](#5-score-and-scoreconfig)).

#### 6.2 Attribute value size cap (Normative) — F5

An adapter MUST enforce a per-attribute-value size cap. The cap is **configurable**
with a default of **16 KB** (serialized-UTF-8 bytes). When an attribute value
exceeds the cap, the adapter MUST truncate that value and stamp the span with the
data-quality signal `llmobs.dq.truncated_attributes = true`
([Data-quality](#9-data-quality-signals) §3), incrementing the counter. Large payloads (embedding
vectors, big blobs) SHOULD be sent as **media references** (§7) rather than inline
attribute values. The cap MUST NOT apply to promoted or identity/frozen fields.

#### 6.3 Attribute key sanitization (Normative)

Attribute **keys** MUST NOT contain ASCII control characters (`U+0000`–`U+001F`
and `U+007F`). A normalizer MUST reject such a key: the offending characters are
stripped to produce the sanitized key, the **original** key is preserved under
`llmobs.raw.attr_key.<sanitized>` (so nothing is lost, invariant 6), and the
data-quality counter `llmobs.dq.sanitized_attribute_keys` is incremented
([Data-quality](#9-data-quality-signals)). Sanitization runs in the shared `normalize` stage, so it
is identical across every transport (the LM-11 discipline, §4.2).

> Rationale: control characters in keys have no legitimate source and break
> downstream encodings. A canonical adapter MAY use a control character as an
> internal field-path separator ([Adapter guidance](#10-adapter-guidance)); forbidding them in
> keys at normalize time makes that encoding collision-free *by construction*
> rather than by escaping. The prohibition is a property of the canonical model,
> not of any one adapter.

### 7. Media references (Normative) — LM-10

Binary/media payloads are handled **out of band**. A span field (`input`,
`output`, or an `attributes` value) MAY contain one or more **media reference
tokens** in place of inline bytes.

#### 7.1 Token format (Normative)

A media reference token MUST match exactly:

```
@@@llmobsMedia:<sha256-hex>@@@
```

where `<sha256-hex>` is the lowercase hex SHA-256 of the referenced content. The
token resolves to a **MediaReference** entity keyed `(project_id, sha256)`. Bytes
are uploaded and fetched via a separate media API (a kernel primitive, specified
elsewhere); this model defines only the token and the MediaReference entity:

| MediaReference field | Type | Null? | Semantics |
|---|---|---|---|
| `project_id` | string | no | Tenant scope (part of identity). |
| `sha256` | string | no | Lowercase hex content hash (part of identity; dedup key). |
| `content_type` | string | yes | MIME type. |
| `size_bytes` | integer≥0 | yes | Payload size. |
| `created_at` | timestamp | no | First-seen time. |

Content addressing by `(project_id, sha256)` gives automatic dedup of identical
media within a project.

> Evidence: Langfuse replaces blobs with `@@@langfuseMedia:...@@@` reference tokens,
> stores bytes in a dedicated S3 bucket via a presigned-URL endpoint, and dedups in
> Postgres by `(projectId, sha256Hash)`; ClickHouse stores only references (study
> Ch. 04 §7; digest §10). This model adopts the token+content-address pattern under
> the LLMObs brand token.

#### 7.2 Oversized-payload truncation (Normative)

An adapter MAY truncate an oversized `input`/`output` payload **only** if it
stamps the span with the data-quality signal `llmobs.dq.truncated = true`
([Data-quality](#9-data-quality-signals)) and increments the truncation counter. **Silent truncation
is prohibited.** An adapter MUST NOT truncate any promoted field or any
identity/frozen field.

> Evidence: Langfuse's `ClickhouseWriter` truncates oversized fields on a size error
> (once) with no per-row marker (study Ch. 04 §10). LM-10 diverges: truncation is
> permitted but MUST be observable via a stamped flag and a counter — never silent.

---

## 4. Trace

**Section type:** Normative except where marked *Informative*.

A **Trace** is the root of one end-to-end unit of work. It is an aggregation over
the spans that share its `id` as their `trace_id`. A trace carries a small set of
trace-level promoted fields and dimensions; the detail lives in its spans.

### 1. Field reference (Normative)

| Field | Type | Null? | Frozen? | Semantics |
|---|---|---|---|---|
| `id` | string | no | **yes** | Trace identity; OTel `trace_id` hex where present ([Entities & identity](#2-entities-and-identity) §3). |
| `project_id` | string | no | **yes** | Tenant scope. |
| `name` | string | yes | no | Human-readable trace name. |
| `start_time` | timestamp | no | **yes** | Trace start; the identity/merge anchor ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm)). |
| `end_time` | timestamp | yes | no | Trace end. MAY be derived by an adapter as `max(span.end_time)`; §3. |
| `status` | object | no | no | OTel-aligned `{code, message?}` ([Span](#3-span) §4.1); defaults `{code:"unset"}`. |
| `input` | opaque | yes | no | Trace-level input ([Span](#3-span) §4.3); opaque. |
| `output` | opaque | yes | no | Trace-level output; opaque. |
| `tags` | array<string> | no | no | §2. Defaults to `[]`. Union-merged on update. |
| `environment` | string | no | **yes** | Sanitized dimension; defaults `"default"` ([Span](#3-span) §4.2, [Data-quality](#9-data-quality-signals) §2). |
| `release` | string | yes | no | Build/deployment identity. |
| `version` | string | yes | no | Per-call logic/prompt version. |
| `session_id` | string | yes | no | Session dimension ([Entities & identity](#2-entities-and-identity) §4.1). |
| `user_id` | string | yes | no | User dimension. |
| `attributes` | map<string, value> | no | no | Open map; raw attributes preserved ([Span](#3-span) §6). Defaults `{}`. |
| `total_cost` | decimal \| null | yes | no | **Derived** trace-level cost: `SUM(span.total_cost)` over the trace's **non-aggregate** spans ([Usage & cost](#7-usage-and-cost) §7.1 — an `agent_step`/`tool_call` span's cost duplicates its child model calls, so it is excluded to avoid double-counting). Null when the trace has no leaf cost. A query-time derived field like `end_time`/`span_count`. Both adapters round the roll-up to a shared fixed decimal scale (Postgres sums exact `NUMERIC`, ClickHouse accumulates `Float64`), so the result is **byte-identical** cross-adapter — conformance-tested, not merely within a tolerance. |

- A trace has **no** `kind` and **no** generation fields — those are span-only.
- Trace `input`/`output`/`status` are independent of any span's; they are set by
  trace-level events or by a root span carrying trace-level updates (§3).

> Evidence: Langfuse's `traces` table carries `id, timestamp, name, user_id,
> metadata, release, version, project_id, environment, public, bookmarked, tags,
> input, output, session_id` (study Ch. 05 §1). This model keeps the equivalent
> promoted/dimension set, drops UI-only flags (`public`, `bookmarked`) to plugin `kv`
> ([Entities & identity](#2-entities-and-identity) §4.1), and folds `metadata` into the general `attributes` map.

### 2. Tags (Normative)

`tags` is a set of free-form strings modeled as an array with **set semantics**:
order is not significant and duplicates are not observable. On update, `tags` is
**union-merged** — a later event adds tags, it does not replace the set
([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm) §2). There is no tag removal in `v1alpha1`; tag
retraction, if needed, is a future additive capability.

### 3. Trace ↔ span relationship (Normative)

A trace MAY be produced two ways, which MUST be observably equivalent:

1. **Explicitly** — an ingested trace event carrying trace-level fields.
2. **Derived from a root span** — when a span with no `parent_span_id` (or one
   flagged as the trace root by the source dialect) carries trace-level fields,
   the normalizer MUST emit a corresponding trace update. A trace MUST exist for
   every `trace_id` referenced by any span; if only spans are ingested, the
   adapter MUST materialize a trace bearing that `id`, its `project_id`,
   `environment`, and `start_time` (a "shallow" trace), leaving other fields
   null/default until a trace-level event populates them.

Trace-level fields set by a root span (`name`, `input`, `output`, `session_id`,
`user_id`, `release`, `version`, `tags`) are applied to the trace via the same
merge algorithm as any other update ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm)). A span's own
fields are never overwritten by this promotion, and vice versa: trace and span
are distinct entities with independent field sets.

`end_time` at the trace level is OPTIONAL and MAY be derived by an adapter as the
maximum `end_time` over the trace's spans. Because this is a derivation over a set
that grows as spans arrive, it is an adapter concern; the logical model does not
require a stored trace `end_time`, and the Query API MUST return a consistent
value regardless of which adapter computes it.

> Evidence: Langfuse reshapes a flat OTLP span stream into a trace-plus-observations
> tree, emitting a `trace-create` when a span is a root, carries trace-level updates,
> or introduces a new `trace_id`, and refreshing a "shallow" trace (id/timestamp/
> environment) otherwise; a wrapper trace is synthesized when an observation has no
> `trace_id` (study Ch. 05 §5; Ch. 06 §4). This model requires the same
> trace-materialization behavior while keeping trace and span as independent
> entities with their own merge state.

### 4. Identity, freezing, merging (Normative)

Trace identity, frozen fields, and the merge algorithm are defined once in
[Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm) and apply to traces exactly as to spans. Trace frozen
fields: `id`, `project_id`, `start_time`, `environment` ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm)
§3). The trace idempotency key is `(project_id, id)` ([Entities & identity](#2-entities-and-identity) §3.2).

---

## 5. Score and ScoreConfig

**Section type:** Normative except where marked *Informative*.

A **Score** is a named, typed **measurement** attached to a subject. It is the
kernel's evaluation primitive. A **ScoreConfig** is the optional definition that
constrains scores of a given name and type.

### 1. A score is a measurement only (Normative) — LM-3

`v1alpha1` scores are measurements. Free-text feedback and corrected model
outputs are **NOT** scores; they are a future plugin-owned `annotation` concept
([Entities & identity](#2-entities-and-identity) §4.3). Consequently the score value space is exactly three data
types (§2); there is no `TEXT` or `CORRECTION` score type.

> Evidence: Langfuse overloads its score record with five data types — adding TEXT
> and CORRECTION — then must exclude those two from every metric via
> `AGGREGATABLE_SCORE_TYPES` / `LISTABLE_SCORE_TYPES` allow-lists, and forces a
> CORRECTION's name to `"output"` and its numeric `value` to a sentinel `0` (study
> Ch. 07 §3, §4, §8). LM-3 rejects the overload: a score is a measurement; corrected
> outputs and free text are a different concept owned by a plugin. See ADR-0017.

#### 1.1 Scores enter only through the score write path (Normative) — Q3

A **normalizer MUST NOT synthesize Score entities** from span attributes. A
normalizer cannot verify a score's `source` (§4) or its subject (§5), so
fabricating scores from, say, an `EVALUATOR` span or an OpenInference
`retrieval.documents.N.document.score` would invent provenance the model
guarantees. An evaluator's *execution* is a span; its *result* is a Score written
by whoever ran it, through the score write path, with a verified `source` and
subject ([Span](#3-span) §2.4).

**Per-document retrieval relevance is not a Score.** A retriever's per-document
scores are payload details of that retrieval (they live in the span's `output`),
not project-level measurements attached to a subject. They MUST NOT be promoted to
Score entities (design finding Q2).

### 2. Value model (Normative) — LM-3

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

### 3. ScoreConfig — definition vs record split (Normative)

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

### 4. Source (Normative) — LM-3

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

### 5. Subjects — what a score attaches to (Normative) — LM-8

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
| `config_ref` | reference | yes | Optional `(type,id,label?)` to a ScoreConfig ([References](#8-references)). |
| `comment` | string | yes | Free-text note attached to the measurement. |
| `metadata` | map<string,value> | no | Open map; defaults `{}`. |
| `timestamp` | timestamp | no | When the measurement was taken; identity/merge anchor. |
| `environment` | string | no | Sanitized dimension; frozen; defaults `"default"`. |

#### 5.1 Kernel-defined subject types (Normative)

The kernel defines exactly these `subject_type` values:

| `subject_type` | `subject_id` refers to |
|---|---|
| `span` | A Span's `id` (the score MAY also, informally, concern the span's trace). |
| `trace` | A Trace's `id`. |
| `session` | A `session_id` value (a session is a dimension, not an entity — [Entities & identity](#2-entities-and-identity) §4.1). |

#### 5.2 Plugin-registered subject types (Normative)

A plugin MAY register additional subject types under its own **namespace**,
formatted `<plugin-namespace>/<name>` (for example `evals/dataset_run_item`). The
registration mechanism is abstract here: a plugin declares, through the plugin
registry (a kernel capability specified elsewhere), the namespaced subject types
it owns. Rules:

- A namespaced subject type MUST contain exactly one `/` separating a non-empty
  plugin namespace from a non-empty name.
- The kernel MUST NOT interpret the semantics of a plugin subject type; it stores
  and returns `(subject_type, subject_id)` verbatim and treats the reference as
  possibly-dangling ([References](#8-references)).
- Kernel subject types (`span`, `trace`, `session`) MUST NOT contain `/` and are
  reserved.

> Evidence: Langfuse hardcodes four attachment points as nullable columns
> (`trace_id` + optional `observation_id`, `session_id`, `dataset_run_id`) enforced
> by a zod refine requiring exactly one, and had to add each new attachment as a
> schema migration (`0012` session, `0017` dataset run) with its own index (study
> Ch. 07 §1, §5). LM-8 generalizes to a `(subject_type, subject_id)` pair so new
> attachment points — especially plugin-owned ones like dataset run items — need no
> kernel schema change.

#### 5.3 Dataset linkage is plugin guidance, not kernel spec (Informative)

Datasets, runs, and run items are owned by the evals plugin ([Entities & identity](#2-entities-and-identity)
§4.2). A dataset-run-item score attaches via `subject_type = "evals/dataset_run_item"`.
The Langfuse reproducibility patterns are recommended plugin guidance:

> Evidence: Langfuse's `dataset_run_items_rmt` denormalizes point-in-time snapshots
> of the run and the dataset item (input/expected-output/metadata, plus the item's
> SCD-2 `validFrom` version) into every row, so an experiment result stays
> reproducible after the item is edited (study Ch. 03; Ch. 10; digest §8). A plugin
> implementing datasets SHOULD adopt snapshot-into-fact-row + versioned items for the
> same reason; the kernel does not mandate or store it.

### 6. Identity, merge, uniqueness (Normative)

- Score idempotency key: `(project_id, id)` ([Entities & identity](#2-entities-and-identity) §3.2).
- Frozen fields: `id`, `project_id`, `subject_type`, `subject_id`, `timestamp`,
  `environment` ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm) §3).
- Logical uniqueness of a human annotation (one score per subject per name/config)
  is NOT a storage constraint in this model; a producer that wants
  overwrite-not-accumulate semantics MUST reuse a stable `id` for the same
  `(subject, name, config)` so the idempotency key collapses re-annotations.

> Evidence: Langfuse enforces annotation uniqueness in application code, not storage:
> its annotation router looks up an existing score by `(project, target, name/config,
> dataType)` and reuses that row's id (study Ch. 07 §6). This model keeps uniqueness a
> producer responsibility via stable ids, and — unlike Langfuse — its idempotency key
> is `(project_id, id)` alone, so a reused id reliably overwrites regardless of
> `timestamp` or `name` drift ([Entities & identity](#2-entities-and-identity) §3.3).

### 7. Validation failure handling (Normative)

Config-validation failure MUST be handled **identically** regardless of ingestion
path (synchronous public API or asynchronous batch). A score that fails
validation MUST be rejected observably (surfaced as an error and counted via a
data-quality signal, [Data-quality](#9-data-quality-signals)); it MUST NOT be silently dropped.

> Evidence: In Langfuse the *same* config validation returns a 400 synchronously but
> is silently swallowed (logged and dropped) on the async batch path (study Ch. 07
> §4, §8). This model prohibits the divergence: one validation, one observable
> outcome.

---

## 6. Update semantics, idempotency, and the merge algorithm

**Section type:** Normative. This is the load-bearing chapter: two independent
adapters MUST implement it identically. §6 (test vectors) is executable data and
is the conformance instrument.

### 1. The event model (Normative)

Entities are not mutated in place at the logical level. An entity's observable
state is the deterministic **fold** of the ordered set of **events** that target
it. Each event is an envelope:

| Envelope field | Type | Semantics |
|---|---|---|
| `event_id` | string | Stable unique id of *this event*. Re-delivery of the same event MUST carry the same `event_id`. Used only as a deterministic tie-breaker (§2). |
| `event_ts` | timestamp | The producer-stamped logical version of this event. Re-delivery MUST carry the same `event_ts`. |
| `target` | (entity_type, project_id, id) | Which entity this event updates ([Entities & identity](#2-entities-and-identity) §3.2). |
| `op` | `upsert` \| `delete` | §4. |
| `payload` | partial field set | For `upsert`: the fields this event sets. Absent fields set nothing. |

An adapter MAY implement this as literal event storage (event-sourced) or as an
in-place upsert that reproduces the same fold — the choice is invisible to the
Query API ([Overview](#1-overview) §1.1).

**`event_ts` for stamp-less transports (Normative).** A transport that carries a
producer version stamp (e.g. a future `langfuse-compat` dialect) MUST use that
stamp as `event_ts`. A transport that does **not** carry one — notably OTLP —
MUST derive `event_ts` **deterministically from payload content**: for OTLP a
span's `event_ts` is its `end_time` when set, else its `start_time`. Determinism
is the point: the same source event always yields the same `event_ts`, so
re-delivery folds to the same state (idempotency, [Entities & identity](#2-entities-and-identity) §3.2). A
normalizer MUST NOT stamp `event_ts` from wall-clock receipt time.

> Evidence: Langfuse models every update and delete as an insert into a
> `ReplacingMergeTree(event_ts, is_deleted)` and reconciles at read time; its worker
> also does a read-modify-write that folds all events for an entity into one merged
> record via `overwriteObject` (study Ch. 03; Ch. 04 §6, §8; Ch. 06 §2). This model
> lifts that behavior into a storage-neutral fold so the lite (in-place UPDATE) and
> scale (ReplacingMergeTree) adapters produce identical results.

### 2. The fold (Normative)

State is computed **per field-group**. A field-group is the unit of merge:

- Each **scalar** promoted/dimension field is its own field-group
  (`name`, `status`, `end_time`, `model`, `total_cost`, …). An **object-valued**
  scalar (e.g. `status = {code, message}`) is a **single** field-group replaced
  **wholesale** — it is NOT deep-merged. So a later `status: {code: "error"}`
  replaces the whole object and drops a prior `message`; this is deliberate
  (a stale `ok`-era message on an errored span is worse than no message). Only
  the map fields (next bullet) deep-merge (V17).
- Each **key** of a map field (`attributes`, `model_parameters`,
  `usage_details`, `cost_details`, `metadata`, …) is its own field-group,
  recursively for nested objects (deep merge). The unit is the leaf key path.
- `tags` is a single field-group with **union** semantics (§3).
- `events` (span events, [Span](#3-span) §4.4) is a single field-group with
  **union** semantics (§3.1).
- `is_deleted` is a field-group (§4).

For every field-group **g**, define the set `S(g)` = events whose `op = upsert`
that **set** g (i.e. provide a non-empty value for g; see §2.1). The entity's
value for g is the value from the event in `S(g)` with the greatest
**`(event_ts, event_id)`** in lexicographic order (`event_ts` first; `event_id`
breaks exact ties). If `S(g)` is empty, g takes its declared default
([Span](#3-span)/[Trace](#4-trace)/[Score & ScoreConfig](#5-score-and-scoreconfig)) — for most fields, null/absent.

This fold is **commutative, associative, and idempotent**: applying the same
events in any order, or applying an event more than once, yields the same state.
Re-delivery is therefore a no-op, satisfying idempotency ([Entities & identity](#2-entities-and-identity) §3.2).

#### 2.1 Empty never clobbers (Normative)

An event does **not** set field-group g (does not join `S(g)`) when its payload
value for g is any of: absent, JSON `null`, the empty string `""`, an empty array
`[]`, or (for a map) a key that is absent. A **composite/object** value is
likewise empty when **every one of its leaves is unset** by this same rule,
applied recursively — e.g. `status: {code: null}` does not set the `status`
field-group, because its only leaf is null. Therefore a later event carrying a
null/empty/absent value, or a composite with no set leaves, MUST NOT overwrite a
previously-set value. Clearing a value is not expressible in `v1alpha1` (it would
be a future explicit-tombstone-per-field capability).

> Evidence: Langfuse's `overwriteObject` merge rule is "empty/undefined never
> clobbers a set value; metadata deep-merges; tags union" (study Ch. 04 §6; Ch. 06
> §2; digest §5). This model adopts that rule verbatim as the normative fold.

### 3. Tags union (Normative)

`tags` (trace-level, [Trace](#4-trace) §2) is the set union of the `tags` arrays over
all `upsert` events in `S(tags)`. Order is not significant; duplicates are not
observable. Tags are never removed by an update in `v1alpha1`.

#### 3.1 Span events union (Normative) — Q1

`events` (span events, [Span](#3-span) §4.4) is the set union of the `events` arrays
over all `upsert` events in `S(events)`. Two span events are the **same** (and
deduplicated) when their `name`, `timestamp`, and `attributes` are all equal; the
observable order is by `timestamp` (ties broken deterministically by `name`).
Because identical span events collapse, re-delivery of an event carrying the same
span events is idempotent (§2). Span events are never removed by an update in
`v1alpha1`.

### 4. Deletion and tombstones (Normative)

- A `delete` event sets the `is_deleted` field-group to `true` at its
  `(event_ts, event_id)`. An `upsert` event sets `is_deleted` to `false` at its
  `(event_ts, event_id)`.
- `is_deleted` follows the same greatest-`(event_ts, event_id)`-wins fold. Hence
  an `upsert` with a greater `(event_ts, event_id)` than a prior `delete`
  **resurrects** the entity, and a `delete` with a greater stamp than all upserts
  tombstones it. Stated directionally: **a tombstone loses to any later revival
  by `(event_ts, event_id)`, and a revival loses to any later tombstone** — it is
  strictly the greatest stamp that decides, never the operation kind (V8–V10).
- A tombstoned entity (`is_deleted = true`) MUST NOT be returned by default Query
  API reads.

### 4a. Soft deletion vs. erasure suppression (Normative)

Two different mechanisms both keep data from being read; they MUST NOT be
conflated — they have opposite guarantees:

- **Soft delete (`is_deleted`, §4)** is an *in-model, mergeable* state. It is a
  field-group carried by a `delete` event, folds by greatest `(event_ts,
  event_id)`, and is **revivable**: a later `upsert` with a greater stamp
  legitimately un-deletes the entity. The row and its payload still exist; they are
  merely hidden from default reads. This is normal update semantics.
- **Erasure suppression (GDPR)** is an *out-of-band, ingest-blocking* mechanism. An
  erasure hard-deletes the matching rows (payloads removed, not hidden) and records
  a **suppression tombstone** keyed by `(project_id, id)`. The persist path MUST
  refuse any re-delivered event whose key matches an unexpired suppression — a
  re-delivery **does not** fold and **does not** revive; it is dropped (and SHOULD
  be counted). Suppression therefore does **not** obey the greatest-stamp rule: it
  is not a merge state at all, and no later `upsert`, however high its stamp, can
  resurrect an erased entity while the tombstone stands.
- **Tombstone lifetime.** A suppression tombstone need only outlive plausible
  redelivery (retry, Collector/Kafka replay), not persist forever; an adapter MAY
  reap tombstones after a bounded retention window. After reaping, the erased key
  is once again a first-sight id — acceptable because redelivery of long-erased
  data is not a realistic vector.

In short: `is_deleted` answers "is this currently deleted?" (and can flip back);
erasure suppression answers "was this key erased for compliance?" (and must not
come back). An adapter that treats a GDPR erasure as a soft delete is
non-conformant.

### 5. Frozen fields (Normative) — LM-5, LM-6

The following fields are **frozen**: their value is fixed by the **first** event
that sets them and never changes. "First" means the event with the **earliest
`(event_ts, event_id)`** (lexicographic, §2) among the events that set the field —
so the frozen value is well-defined regardless of arrival order.

| Entity | Frozen fields |
|---|---|
| Span | `id`, `project_id`, `trace_id`, `kind`, `start_time`, `environment` |
| Trace | `id`, `project_id`, `start_time`, `environment` |
| Score | `id`, `project_id`, `subject_type`, `subject_id`, `timestamp`, `environment` |

When a later event offers a **different** value for a frozen field, the adapter
MUST:

1. **Keep** the original (first-written) value — the frozen value is not changed.
2. **Preserve** the offered value in `attributes` under the reserved key
   `llmobs.raw.<field>` (e.g. `llmobs.raw.start_time`).
3. **Increment** the data-quality counter `llmobs.dq.frozen_field_conflict`
   ([Data-quality](#9-data-quality-signals)), tagged with the field name.
4. **NOT reject** the rest of the event — all non-frozen field-groups in the same
   event MUST still be folded normally.

`environment` frozen-conflict handling composes with dimension sanitization
([Data-quality](#9-data-quality-signals) §2): the offered value is sanitized first, then compared.

> Evidence: In Langfuse any field in the storage sort key is effectively immutable —
> changing it splits the entity into two physical rows — and this is an unmanaged
> hazard (study Ch. 04 §6; Ch. 07 §6; digest §5, §6). LM-5/LM-6 make immutability
> explicit and *managed*: frozen fields are enumerated, a conflicting update is
> absorbed (original kept, offered preserved, counter raised) rather than silently
> creating a divergent entity. In particular a mismatched `start_time` MUST be
> normalized to the original (LM-6), never allowed to move the entity.

### 6. Normative test vectors (Normative)

The following vectors define required merge behavior as **executable data**:
each is a JSON object `{ name, entity, note, events, expect }`. `events` is the
ordered set of ingested events to fold (each `{op, event_ts, event_id?, payload}`);
`expect` is the required observable state (with `is_deleted`). An implementation
MUST reproduce `expect` exactly. `event_ts` and `event_id` follow §2; the fold is
order-independent, so a vector's `events` MAY be applied in any order. Frozen-field
conflicts surface `expect.attributes["llmobs.raw.<field>"]` and
`expect.dq["frozen_field_conflict.<field>"]` (§5, [Data-quality](#9-data-quality-signals)). `tags` and
`events` compare as sets/dedup-ordered. The conformance suite (`tools/conformance`)
parses these blocks and runs them against every storage adapter.

#### V1 — empty never clobbers a scalar (null offered leaves value; new field set)

```json
{
  "name": "V1",
  "entity": "span",
  "note": "empty never clobbers a scalar (null offered leaves value; new field set)",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "kind": "span",
        "name": "plan",
        "start_time": 1,
        "status": {
          "code": "ok"
        }
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "name": null,
        "status": {
          "code": null
        },
        "output": "done"
      }
    }
  ],
  "expect": {
    "kind": "span",
    "name": "plan",
    "start_time": 1,
    "status": {
      "code": "ok"
    },
    "output": "done",
    "is_deleted": false
  }
}
```

#### V2 — later non-empty scalar wins

```json
{
  "name": "V2",
  "entity": "span",
  "note": "later non-empty scalar wins",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "name": "plan",
        "status": {
          "code": "unset"
        }
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "name": "replan",
        "status": {
          "code": "ok"
        }
      }
    }
  ],
  "expect": {
    "name": "replan",
    "status": {
      "code": "ok"
    },
    "is_deleted": false
  }
}
```

#### V3 — out-of-order event loses per field-group

```json
{
  "name": "V3",
  "entity": "span",
  "note": "out-of-order event loses per field-group",
  "events": [
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "name": "replan"
      }
    },
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "name": "plan",
        "output": "x"
      }
    }
  ],
  "expect": {
    "name": "replan",
    "output": "x",
    "is_deleted": false
  }
}
```

#### V4 — attributes deep-merge, per-key latest wins

```json
{
  "name": "V4",
  "entity": "span",
  "note": "attributes deep-merge, per-key latest wins",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "attributes": {
          "a": 1,
          "nested": {
            "p": true,
            "q": 1
          }
        }
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "attributes": {
          "b": 2,
          "nested": {
            "q": 9
          }
        }
      }
    }
  ],
  "expect": {
    "attributes": {
      "a": 1,
      "b": 2,
      "nested": {
        "p": true,
        "q": 9
      }
    },
    "is_deleted": false
  }
}
```

#### V5 — tags union (order not significant; compared as a set)

```json
{
  "name": "V5",
  "entity": "trace",
  "note": "tags union (order not significant; compared as a set)",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "tags": [
          "prod",
          "eu"
        ]
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "tags": [
          "eu",
          "canary"
        ]
      }
    }
  ],
  "expect": {
    "tags": [
      "canary",
      "eu",
      "prod"
    ],
    "is_deleted": false
  }
}
```

#### V6 — frozen field conflict (start_time) absorbed, not applied

```json
{
  "name": "V6",
  "entity": "span",
  "note": "frozen field conflict (start_time) absorbed, not applied",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "id": "s1",
        "kind": "span",
        "start_time": 1,
        "environment": "prod"
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "start_time": 5,
        "name": "n"
      }
    }
  ],
  "expect": {
    "id": "s1",
    "kind": "span",
    "start_time": 1,
    "environment": "prod",
    "name": "n",
    "attributes": {
      "llmobs.raw.start_time": 5
    },
    "dq": {
      "frozen_field_conflict.start_time": 1
    },
    "is_deleted": false
  }
}
```

#### V7 — frozen field conflict (kind) absorbed; non-frozen field still applied

```json
{
  "name": "V7",
  "entity": "span",
  "note": "frozen field conflict (kind) absorbed; non-frozen field still applied",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "id": "s1",
        "kind": "generation",
        "start_time": 1
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "kind": "tool_call",
        "model": "gpt-x"
      }
    }
  ],
  "expect": {
    "id": "s1",
    "kind": "generation",
    "start_time": 1,
    "model": "gpt-x",
    "attributes": {
      "llmobs.raw.kind": "tool_call"
    },
    "dq": {
      "frozen_field_conflict.kind": 1
    },
    "is_deleted": false
  }
}
```

#### V8 — tombstone hides the entity

```json
{
  "name": "V8",
  "entity": "span",
  "note": "tombstone hides the entity",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "name": "plan"
      }
    },
    {
      "op": "delete",
      "event_ts": 2
    }
  ],
  "expect": {
    "name": "plan",
    "is_deleted": true
  }
}
```

#### V9 — resurrection: upsert after delete wins by event_ts

```json
{
  "name": "V9",
  "entity": "span",
  "note": "resurrection: upsert after delete wins by event_ts",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "name": "plan"
      }
    },
    {
      "op": "delete",
      "event_ts": 2
    },
    {
      "op": "upsert",
      "event_ts": 3,
      "payload": {
        "output": "done"
      }
    }
  ],
  "expect": {
    "name": "plan",
    "output": "done",
    "is_deleted": false
  }
}
```

#### V10 — stale delete loses to newer upsert

```json
{
  "name": "V10",
  "entity": "span",
  "note": "stale delete loses to newer upsert",
  "events": [
    {
      "op": "upsert",
      "event_ts": 3,
      "payload": {
        "output": "done"
      }
    },
    {
      "op": "delete",
      "event_ts": 2
    }
  ],
  "expect": {
    "output": "done",
    "is_deleted": false
  }
}
```

#### V11 — idempotent re-delivery (same event twice == once)

```json
{
  "name": "V11",
  "entity": "span",
  "note": "idempotent re-delivery (same event twice == once)",
  "events": [
    {
      "op": "upsert",
      "event_ts": 2,
      "event_id": "e9",
      "payload": {
        "name": "replan"
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "event_id": "e9",
      "payload": {
        "name": "replan"
      }
    }
  ],
  "expect": {
    "name": "replan",
    "is_deleted": false
  }
}
```

#### V12 — exact event_ts tie broken by event_id (greater wins)

```json
{
  "name": "V12",
  "entity": "span",
  "note": "exact event_ts tie broken by event_id (greater wins)",
  "events": [
    {
      "op": "upsert",
      "event_ts": 2,
      "event_id": "e1",
      "payload": {
        "name": "a"
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "event_id": "e2",
      "payload": {
        "name": "b"
      }
    }
  ],
  "expect": {
    "name": "b",
    "is_deleted": false
  }
}
```

#### V13 — usage map merges per key

```json
{
  "name": "V13",
  "entity": "span",
  "note": "usage map merges per key",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "kind": "generation",
        "provided_usage_details": {
          "input": 100
        }
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "provided_usage_details": {
          "output": 20,
          "total": 120
        }
      }
    }
  ],
  "expect": {
    "kind": "generation",
    "provided_usage_details": {
      "input": 100,
      "output": 20,
      "total": 120
    },
    "is_deleted": false
  }
}
```

#### V14 — score value fields, data_type authoritative; both nullable, no sentinel

```json
{
  "name": "V14",
  "entity": "score",
  "note": "score value fields, data_type authoritative; both nullable, no sentinel",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "data_type": "categorical",
        "value_string": "good",
        "value_numeric": null
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "value_numeric": 1
      }
    }
  ],
  "expect": {
    "data_type": "categorical",
    "value_string": "good",
    "value_numeric": 1,
    "is_deleted": false
  }
}
```

#### V15 — span events union with dedup by (name,timestamp,attributes), ordered by timestamp

```json
{
  "name": "V15",
  "entity": "span",
  "note": "span events union with dedup by (name,timestamp,attributes), ordered by timestamp",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "events": [
          {
            "name": "gen_ai.user.message",
            "timestamp": 1,
            "attributes": {
              "content": "hi"
            }
          }
        ]
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "events": [
          {
            "name": "gen_ai.user.message",
            "timestamp": 1,
            "attributes": {
              "content": "hi"
            }
          },
          {
            "name": "gen_ai.choice",
            "timestamp": 2,
            "attributes": {
              "finish_reason": "stop"
            }
          }
        ]
      }
    }
  ],
  "expect": {
    "events": [
      {
        "name": "gen_ai.user.message",
        "timestamp": 1,
        "attributes": {
          "content": "hi"
        }
      },
      {
        "name": "gen_ai.choice",
        "timestamp": 2,
        "attributes": {
          "finish_reason": "stop"
        }
      }
    ],
    "is_deleted": false
  }
}
```

#### V16 — composite with no set leaves never clobbers (status:{code:null})

```json
{
  "name": "V16",
  "entity": "span",
  "note": "composite with no set leaves never clobbers (status:{code:null})",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "status": {
          "code": "ok",
          "message": "done"
        }
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "status": {
          "code": null
        },
        "name": "n"
      }
    }
  ],
  "expect": {
    "status": {
      "code": "ok",
      "message": "done"
    },
    "name": "n",
    "is_deleted": false
  }
}
```

#### V17 — object-valued scalar (status) replaces wholesale — a later status drops a prior message

```json
{
  "name": "V17",
  "entity": "span",
  "note": "object-valued scalar (status) replaces wholesale \u2014 a later status drops a prior message",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "status": {
          "code": "ok",
          "message": "done"
        }
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "status": {
          "code": "error"
        }
      }
    }
  ],
  "expect": {
    "status": {
      "code": "error"
    },
    "is_deleted": false
  }
}
```

An implementation that reproduces V1–V17 for both the Postgres and ClickHouse
adapters satisfies the update-semantics conformance bar. Additional vectors MAY
be added additively.

---

## 7. Usage and cost

**Section type:** Normative except where marked *Informative*.

Usage and cost apply to `generation_shaped` spans ([Span](#3-span) §5). The model
stores **both** what the client provided **and** what the kernel resolved, keeps
them as open-keyed maps, and records enough provenance to re-derive cost later.

### 1. Fields (Normative)

| Field | Type | Semantics |
|---|---|---|
| `provided_usage_details` | map<string, integer≥0> | Usage counts exactly as sent by the client. |
| `usage_details` | map<string, integer≥0> | Resolved usage (provided, or kernel-derived). §3. |
| `provided_cost_details` | map<string, decimal> | Cost exactly as sent by the client. |
| `cost_details` | map<string, decimal> | Resolved cost (provided, or kernel-derived). §4. |
| `total_cost` | decimal \| null | Scalar total (§4.1). |
| `cost_source` | `provided` \| `derived` \| null | How `cost_details` was obtained (§4). |
| `pricing_snapshot_ref` | reference \| null | The price entry used for derivation (§5). |

All four maps default to `{}`. Usage values MUST be non-negative integers; cost
values are decimals (a fixed-scale decimal, not a binary float, to avoid drift on
money — adapters MUST preserve at least 12 fractional digits).

> Evidence: Langfuse stores paired `provided_usage_details`/`usage_details`
> (`Map(..., UInt64)`) and `provided_cost_details`/`cost_details`
> (`Map(..., Decimal64(12))`) plus a scalar `total_cost`, so it retains both the
> client's numbers and its resolved numbers (study Ch. 05 §1; Ch. 06 §5.1). This
> model keeps the dual maps and the `Decimal64(12)`-equivalent precision, and adds
> `cost_source` + `pricing_snapshot_ref` (§4, §5) for re-derivability.

### 2. Provided maps are the only wire-writable surface (Normative)

A normalizer MUST write usage/cost the client supplied **only** into the
`provided_*` maps. It MUST NOT populate `usage_details`, `cost_details`,
`total_cost`, `cost_source`, or `pricing_snapshot_ref` — those are produced by the
kernel enrichment step (§3, §4), never by a wire format.

### 3. Well-known usage keys and resolution (Normative)

#### 3.1 Well-known keys

The usage/cost maps are open, but these keys are **well-known** and normalizers
SHOULD map onto them so that cross-provider aggregation works:

| Key | Meaning |
|---|---|
| `input` | Input/prompt tokens (or the unit's input count). |
| `output` | Output/completion tokens. |
| `total` | Total tokens; if absent, consumers compute it as the sum of the other keys. |
| `cache_read` | Tokens served from a prompt cache (read). |
| `cache_write` | Tokens written to a prompt cache. |
| `reasoning` | Reasoning/thinking tokens (models that bill these separately). |
| `audio` | Audio tokens/units (unspecified direction). |
| `audio_input` | Audio input tokens (a detail of `input`, priced separately). |
| `audio_output` | Audio output tokens (a detail of `output`, priced separately). |
| `image` | Image tokens/units. |

Additional keys are permitted (the map is open); adding a well-known key is
additive. A normalizer MUST map a provider's name onto a well-known key only where
the rename is a pure alias (e.g. OTel `gen_ai.usage.input_tokens` → `input`,
`output_tokens` → `output`).

**Provider-reported semantics, as-is (Normative, F3).** Usage values are recorded
with the **provider's own semantics** — `input` is whatever that provider calls
input, and cache buckets (`cache_read`/`cache_write`) are **additive detail keys**.
A normalizer MUST NOT reinterpret across providers: it MUST NOT subtract cached
tokens from `input`, MUST NOT synthesize provider-specific fictions, and MUST NOT
assume `input` is net-of-cache or inclusive-of-cache — it stores what the provider
reported. **Cache accounting is not comparable across providers**, and consumers
MUST NOT assume it is. Recording honest provider semantics is preferred over
silently normalizing to a cross-provider fiction.

> Evidence: Langfuse normalizes token names but also **subtracts** cached tokens from
> the input total to avoid double counting (study Ch. 05 §5, Ch. 06 §5.1) — a
> cross-provider reinterpretation that bakes in an assumption about whether `input`
> includes cache. F3 rejects that: the model keeps `cache_read`/`cache_write` as
> first-class additive keys and records provider values verbatim, leaving
> interpretation to the (provider-aware) consumer rather than a lossy normalize step.

#### 3.2 Resolution precedence for `usage_details` (Normative)

The kernel enrichment stage computes `usage_details` as follows:

1. If `provided_usage_details` is non-empty, `usage_details` = a normalized copy
   of it (drop negative/non-integer values; synthesize `total` as the sum of the
   other keys only if no `total` key is present).
2. Otherwise, the kernel MAY derive usage (e.g. tokenization) **only** when a
   model is resolved and the span's `status.code` is not `error`. Derived usage
   populates at least `input`, `output`, `total`.
3. Otherwise `usage_details` is `{}`.

> Evidence: Langfuse tokenizes to fill usage **only if** a model matched, no usage
> was provided, and `level != ERROR`; provided usage is otherwise normalized and a
> missing `total` is synthesized as the bucket sum (study Ch. 06 §5.2). This model
> adopts the same precedence (provided usage > derived usage; skip derivation on
> error).

### 4. Cost resolution (Normative) — LM-4

The kernel enrichment stage computes `cost_details`, `total_cost`, and
`cost_source`:

1. **Provided cost wins and short-circuits derivation.** If
   `provided_cost_details` is non-empty, then `cost_details` = a copy of it,
   `cost_source = "provided"`, and the kernel MUST NOT derive any additional cost
   point. `pricing_snapshot_ref` is null.
2. Otherwise, if a price entry resolves, the kernel derives `cost_details` by
   multiplying each `usage_details[k]` by the matching unit price, sets
   `cost_source = "derived"`, and sets `pricing_snapshot_ref` to the price entry
   used (§5). Price lookup keys on the promoted `provider` and `model` fields
   ([Span](#3-span) §5, F7) — the served model, not the requested one — so the
   derivation inputs are typed fields rather than attribute-map lookups.
3. Otherwise `cost_details = {}`, `total_cost = null`, `cost_source = null`.

#### 4.1 `total_cost` (Normative)

`total_cost` is the scalar total. When `cost_details` has a `total` key,
`total_cost` equals it; otherwise it is the sum of the `cost_details` values.
`total_cost` is `null` when `cost_details` is empty.

> Evidence: Langfuse's provided cost short-circuits all derivation (a single provided
> cost key disables price-table lookup), otherwise it multiplies `usage_details[k]` by
> the tier price for `usageType == k` and sums a missing `total`; `total_cost` is the
> denormalized scalar (study Ch. 06 §5.2 step 4, §6). LM-4 keeps provided-wins
> precedence but records `cost_source` so a consumer can tell provided from derived —
> which Langfuse cannot distinguish after the fact.

### 5. Pricing snapshot and re-derivability (Normative) — LM-4

Every **derived** cost MUST record a `pricing_snapshot_ref`: a reference
([References](#8-references)) identifying the exact price entry (its id and version) used
for the derivation. This makes historical cost **re-derivable**: a later price
correction does not silently invalidate stored costs, and a re-pricing run can
recompute `cost_details` for the affected spans.

**Encoding (Normative) — ADR-0029.** The price table (`api/schemas/pricing/v1alpha1`)
stores price entries as immutable, versioned rows keyed `(provider, model, version)`,
whose primary key `id = "<provider-canonical>/<model-canonical>#<version>"`. The
`pricing_snapshot_ref` is `{ type: "price", id: <that entry id>, label:
"<provider>/<model> v<version>" }`. Because a version row is immutable and carries its
**whole** rate schedule — base rates, per-bucket detail rates, AND the tier schedule
(§7.5) — the single `id` fully identifies everything needed to re-derive; the tier
schedule version is the entry version, not a separate field. A re-pricing backfill
(below) matches spans by `pricing_snapshot_ref.id` and re-derives against the newest
applicable version.

Re-pricing MUST be performed as a **kernel background backfill job** (not an inline
mutation): it reads spans whose `pricing_snapshot_ref.id` matches a superseded price
version (or, for a discount change, a project's derived spans) and re-emits `upsert`
events recomputing `cost_details`, `total_cost`, and `pricing_snapshot_ref`. Each
re-emitted event carries **only** the cost field-groups with a fresh `event_ts`, so
the merge fold ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm)) updates cost and leaves every other
field-group at its original provenance. Because re-pricing MUTATES money across a time
range, it MUST reuse the lite→scale backfill discipline (ADR-0026): bounded chunks; a
`(ts, project_id, id)` total-ordered resumable cursor; a SEPARATE generous execution
budget (never the interactive read timeout); and the permanent-vs-transient failure
taxonomy (CLAUDE.md #12) — a transient price-store/persist failure STOPS the run loud
and resumable (it is never converted into a per-span null, which here would DROP an
existing cost), while a deterministic per-span anomaly is dead-lettered and stepped
over. It MUST be **idempotent**: a span whose re-derived cost equals its current cost
is not re-emitted (re-derivation is bit-stable, §7.6), so a second identical run is a
no-op. Derivation MUST reuse the SAME path as the ingest enrich stage (never a fork),
so a re-priced span computes byte-identically to one priced at ingest — including
**cross-adapter** (Postgres and ClickHouse yield identical re-priced cost). A re-price
scoped to one project (a discount change) MUST NOT cross the project boundary. The
run's cursor/dead-letter state is control-plane (Postgres in both profiles); the scan
runs against the store the spans live in (either adapter). The trigger is an
admin-gated control-plane endpoint (`POST /v1alpha1/pricing/reprice`), authorized
exactly like a price/discount edit.

> Evidence: In Langfuse cost is resolved at ingest against a point-in-time price
> table and stored denormalized, so a price change or mis-match requires reprocessing
> history — which it does via dedicated background migrations
> (`addGenerationsCostBackfill`) — but it stores **no** reference to which price row
> produced a given cost, making the backfill a blunt full-rewrite (study Ch. 06 §5.2,
> §6; digest §4). LM-4 diverges by storing `pricing_snapshot_ref` so re-pricing is
> targeted and history is precisely re-derivable, and by running it as a resumable,
> budgeted kernel background backfill job (reusing the lite→scale backfill discipline)
> rather than a bespoke one-shot migration.

### 6. Read-time reduction (Informative)

Consumers that need scalar `input`/`output`/`total` usage or cost from the open
maps compute them by summing keys by well-known prefix (all `input*` → input,
etc.) and reading `total` directly. This is a presentation reduction; the
authoritative values are the maps and `total_cost`.

> Evidence: Langfuse's read layer reduces the open maps to `input/output/total`
> scalars by summing keys `startsWith("input")` / `startsWith("output")` and reading
> `total` (study Ch. 06 §5.4). Recorded here as informative guidance for Query API
> implementers; it is not a storage rule.

### 7. Derivation stage rules (Normative) — ADR-0025 / K2

The kernel enrichment stage that derives `usage_details`/`cost_details` (§3.2, §4) is
**not built yet** (`pipeline/stages.go` is a no-op). These rules are pinned before it
exists so it is built correct — each is a failure mode the Langfuse mine observed
shipped. When the stage is built it MUST honor them:

- **7.1 Aggregate spans MUST NOT double-count leaf usage.** An `agent_step` /
  `invoke_agent` span frequently carries the *same* usage as its child model-call
  span. Trace-level cost aggregation (a `SUM(total_cost)` over a trace's spans) MUST
  NOT count both — that doubles the trace's cost. Derivation MUST treat an aggregate
  span's usage as suspect: either it is excluded from trace-level cost roll-ups, or it
  is flagged, but it is never summed alongside a child that carries the same usage.
  (Evidence: Langfuse #14808 zeroes usage on AI-SDK agent spans.)
- **7.2 Never sum `input + cache` as disjoint.** F3 (§3.1) forbids assuming whether
  `input` is inclusive- or net-of-cache. Therefore the synthesized `total` (§3.2.1)
  and any derived cost MUST be computed from `input + output` only; `cache_read`,
  `cache_write`, and `reasoning` are additive **detail** keys and MUST NOT be summed
  into `total` or multiplied-and-added as separate cost lines that also inflate the
  input line. Summing input + cache is the mirror image of Langfuse's
  subtract-cache-from-input bug (#14902/#14945) — F3 protects ingest, this rule
  protects derivation.
- **7.3 `model` without usage MUST NOT fabricate cost.** §3.2.2 permits usage
  derivation only when a model resolves and `status.code != error` — but a
  wrapper/agent span that has a `model` set and **no** provided usage MUST NOT be
  tokenized into estimated usage and then priced. Gate estimation on the span being a
  leaf generation whose usage is genuinely absent (not merely carried by a child),
  or the trace accrues phantom cost. (Evidence: Langfuse #14945 — a model set without
  usage flipped a span into estimated-usage mode.)
- **7.4 EVERY usage detail key is priced at its OWN rate off the price entry; the
  base rate applies only to the residual — pricing is data-driven, never a hardcoded
  case list.** When derivation prices a span, each specially-priced detail bucket
  (`cache_read`, `cache_write`, `reasoning`, `audio`, and any future key) MUST be
  billed at that bucket's rate on the price entry (e.g. `cache_read_input_token_cost`,
  `input_cost_per_audio_token`), and the base input rate applies **only to the
  residual** — `input − Σ(specially-priced buckets)`. Whether a bucket is
  specially priced is decided **solely by the presence of its rate on the price
  entry** — there MUST NOT be a hardcoded set of "providers/buckets that get special
  pricing." One code path prices all providers and all buckets uniformly; adding a
  provider or a new detail key is adding price data, never code.
  *Meta-lesson (both incumbents):* every incumbent cost bug in this class comes from
  **enumerating special cases instead of driving from the price entry** — so a case
  that wasn't enumerated (a provider, a bucket) silently falls through to the flat
  rate. Drive uniformly from data. (Evidence: Opik #5618 bills LiteLLM cache tokens at
  full input rate → 5–10× over-report; Opik #6976 registers a cache calculator for
  anthropic/openai/bedrock only, so Google falls through to flat cost though Gemini
  entries carry cache rates; Opik #7137 never read `input_cost_per_audio_token`, so
  audio prompt tokens — up to 16× the text rate — billed at the text rate. This is the
  mirror of §7.2: Langfuse *over-subtracts* cache from input, Opik *under-discounts*
  it — F3 (§3.1) exists precisely because this surface is provably hard in both
  directions, which is why ingest stays verbatim and only derivation applies rates.)
- **7.5 Tiered / threshold pricing MUST be applied, and the tier schedule MUST be
  captured in the pricing snapshot.** A price entry may carry above-threshold rates
  (e.g. `*_above_200k_tokens`). Derivation MUST bill tokens above the threshold at the
  tier rate, not the flat base rate. The `pricing_snapshot_ref` (§5) MUST record the
  tier schedule version that was applied, or a later re-pricing backfill (§5, a
  jobs-primitive replay) recomputes against a different schedule and silently
  disagrees with the originally-derived cost. (Evidence: Opik #6982 never parses the
  `*_above_200k_tokens` fields, so long-context Gemini 2.5 Pro calls are billed at
  ~half the correct input rate.)
- **7.6 Model-key normalization MUST be symmetric, and provider canonicalization is a
  single table.** The transform applied to a model name when the price table is
  **loaded** MUST be byte-identically applied when the table is **looked up** at
  derivation time — a provider-prefixed name (`openai/gpt-4o`,
  `openrouter/openai/gpt-3.5-turbo`, `anthropic/claude-3-5-sonnet-20241022`) must
  resolve to the same key both ways or the lookup misses and the span silently records
  zero cost. Provider identity (`vertex_ai` vs `gemini` vs `google_ai`; `azure` vs
  `openai`) resolves through ONE canonicalization table shared by pricing and
  credential resolution; the raw provider string is preserved verbatim
  (`semconv.go` promotes `gen_ai.provider.name`/`gen_ai.system` unchanged, D6/§0). A
  conformance fixture MUST prove normalization is symmetric in both directions.
  (Evidence: Opik #5621 strips the prefix at load but not at lookup → all LiteLLM OTel
  spans record `cost = 0`; Opik #6928 routes Vertex AI models to `provider='gemini'`,
  breaking both pricing and credentials.)
- **7.7 Order of operations: REDUCE FIRST, THEN TIER THE RESIDUAL; tiers are
  GRADUATED, never cliff.** This resolves how §7.4 (residual) and §7.5 (tiers)
  compose; both are normative.
  - **7.7.1 Reduce before tiering.** Derivation MUST compute the residual base FIRST
    (§7.4: `residual = base − Σ(specially-priced buckets whose rate declares it
    reduces that base)`), and MUST apply tier breakpoints to that **residual**, never
    to the raw base count. The specially-priced buckets (`cache_read`, `audio`,
    `reasoning`, …) are already billed at their own rates, so counting them toward a
    tier threshold on the raw base double-counts them — the same enumerate-and-double
    failure class §7.4 names, surfacing as a wrong tier boundary. Concretely: with
    `input = 210k`, `cache_read = 30k` (reduces input), and an input tier at `>200k`,
    the residual input is `180k`, which is **below** the 200k breakpoint — so the tier
    does NOT apply; billing the raw `210k` would wrongly cross it. A conformance
    fixture MUST prove this, including the crossover case (residual just-under vs
    just-over the breakpoint) computes correctly.
  - **7.7.2 Graduated (tax-bracket), never cliff.** When a breakpoint is crossed, only
    the tranche of tokens **above** the threshold bills at the tier rate; tokens up to
    the threshold bill at the base rate. Derivation MUST NOT reprice the whole amount
    at the crossed rate (a "cliff"), which produces a discontinuous cost jump at the
    boundary that does not match real provider pricing. For a single breakpoint `T`
    with residual `r > T`: `cost = T·base_rate + (r − T)·tier_rate`.
  - **7.7.3 Single-breakpoint now; graduated multi-breakpoint is a shape-ready
    follow-on.** The price-entry `tiers` shape (an array of `{key, threshold_tokens,
    per_token}`, ADR-0029) and the reduce-then-tier order already support graduated
    pricing across MORE than one breakpoint per key. The first derivation
    implementation MUST implement and fixture the single-breakpoint case (§7.5); true
    graduated multi-breakpoint pricing (≥2 tranches per key, each at its own rate) is
    a tracked follow-on built on the same shape — the spec defines its semantics
    (7.7.2 generalizes tranche-by-tranche) so the implementation is additive, never a
    contract change. Until it lands, an entry SHOULD carry at most one breakpoint per
    key; a second breakpoint's exact multi-tranche arithmetic is not yet guaranteed.

> **Cross-adapter note.** 7.4–7.6 are pinned from a second incumbent (Opik / Comet,
> a Java+ClickHouse codebase architecturally unlike Langfuse). Where a rule cites both
> incumbents (7.1 aggregate double-count: Langfuse #14808 **and** Opik #4695; 7.4
> cache: Langfuse #14902 **and** Opik #5618), two independently-architected systems
> hit the same failure — the strongest signal the immunity must be *conformance-tested*,
> not merely designed. Every `#NNNN` above is a public issue in the incumbent's own
> tracker (`langfuse/langfuse`, `comet-ml/opik`) and is checkable there; the mining
> method and cross-over rates are recorded in `docs/positioning.md` (Evidence appendix).

---

## 8. References

**Section type:** Normative except where marked *Informative*.

Many model fields point at another entity or at a plugin-owned object: a score's
`config_ref`, a generation's `prompt_ref` and `pricing_snapshot_ref`, a score's
`(subject_type, subject_id)`. This chapter defines the single reference shape and
the tolerance rules that apply to all of them.

### 1. Reference shape (Normative) — LM-12

A **reference** is a `(type, id)` pair, OPTIONALLY carrying a **label snapshot**:

| Reference field | Type | Null? | Semantics |
|---|---|---|---|
| `type` | string | no | The referent's kind (e.g. `score_config`, `prompt`, `price`, or a plugin namespaced type `evals/dataset_run_item`). |
| `id` | string | no | The referent's identity within its type and project. |
| `label` | string | yes | A human-readable snapshot of the referent captured **at write time** (e.g. a prompt's name+version). |

- A reference is always scoped to the same `project_id` as the entity that
  carries it; the project is not repeated in the reference.
- There are **no foreign keys** across the entity/plugin boundary. A reference is
  a soft pointer; the referent MAY not exist (§3).

#### 1.1 References are queryable (Normative) — Q6

References stored on an entity MUST be **filterable in the Query DSL by equality**
on `(ref_type, ref_id, ref_label)`. This is a **generic** mechanism: it applies to
every reference field (`prompt_ref`, `pricing_snapshot_ref`, `config_ref`, and any
future plugin-owned reference) uniformly, with no per-reference promotion.

This recovers, generically, the query power a promoted column would give — e.g.
"all generations whose `prompt_ref` has `ref_id = X`" or "… `ref_label =
greeting@v3`" — so Langfuse's filter-/group-by-prompt capability is available
without promoting prompt fields (which would make prompts a kernel concept,
violating invariant 2). Every plugin-owned reference type gets the same filtering
for free.

> Rationale: Q6 keeps prompts (and all cross-boundary referents) plugin-owned while
> restoring the queryability that motivated promoting them. The Query DSL contract
> (a separate document) MUST expose reference-equality filters; this section is the
> data-model obligation that makes references first-class *query targets* without
> making them promoted *storage columns*.

> Evidence: Langfuse makes every cross-store/cross-entity pointer a plain string
> with no FK — eval job inputs, dataset item source ids, run-item pointers, media
> links, observation→prompt — and explicitly dropped the Postgres FKs between
> now-ClickHouse entities; its prompt linkage stores the resolved `(prompt_name,
> prompt_version)` and degrades `prompt_id` to `""` if the prompt row was later
> deleted (study Ch. 02 §12; Ch. 09 §5; digest §12). LM-12 formalizes this as a
> uniform `(type, id, label?)` reference with an explicit label snapshot.

### 2. Label snapshots (Normative)

The OPTIONAL `label` captures a human-readable rendering of the referent as it was
**at the moment the reference was written**. It exists so that a consumer can
display a meaningful reference even when the referent has since changed or been
deleted. The `label` is a snapshot: it is NOT kept in sync with the referent and
MUST NOT be treated as authoritative for anything but display.

A producer SHOULD populate `label` when a human-readable identifier is available
(e.g. `prompt_ref.label = "greeting@v3"`).

### 3. Dangling-reference tolerance (Normative)

A reference MAY dangle: its referent MAY be absent at read time (never created,
or deleted after the reference was written). Consumers MUST tolerate this:

- A consumer MUST NOT error solely because a reference does not resolve.
- When a referent cannot be resolved, a consumer SHOULD fall back to the `label`
  snapshot (if present) for display, or otherwise present the raw `(type, id)`.
- The kernel MUST NOT delete or rewrite an entity merely because one of its
  references became dangling; referential integrity across the boundary is by
  **convention**, maintained by cascade/backfill jobs, not by constraints.

> Evidence: Langfuse consumers must tolerate dangling references by construction —
> integrity depends on cascade + background deletion workers, and prompt linkage
> degrades gracefully when the prompt is gone (study Ch. 02 §12; Ch. 09 §5). This
> model mandates the same tolerance and records that a resolve-or-placeholder helper
> will be provided by the plugin SDK (forward reference; the SDK is out of scope of
> this contract).

### 4. Where references are used (Informative)

| Field | Reference `type` | Notes |
|---|---|---|
| `Score.config_ref` | `score_config` | Optional link to a ScoreConfig ([Score & ScoreConfig](#5-score-and-scoreconfig) §3). |
| `Score.subject` | via `(subject_type, subject_id)` | The subject pair is itself a reference in all but name ([Score & ScoreConfig](#5-score-and-scoreconfig) §5); kernel types `span`/`trace`/`session`, plugin types namespaced. |
| `Span.prompt_ref` | `prompt` | Generation prompt linkage; owned by the prompt-management plugin ([Span](#3-span) §5.4). |
| `Span.pricing_snapshot_ref` | `price` | The price entry used to derive cost ([Usage & cost](#7-usage-and-cost) §5). |
| media reference token | — | Media is referenced by the `@@@llmobsMedia:<sha256>@@@` token, not the `(type,id)` reference shape ([Span](#3-span) §7). |

---

## 9. Data-quality signals

**Section type:** Normative.

Across this specification the kernel **normalizes, coerces, or truncates** input
rather than failing (a resilient-ingestion stance). Every such action MUST be
**observable** — silent lossy behavior is prohibited. This chapter defines the
one consistent mechanism used everywhere, so these are not per-feature
inventions.

### 1. The two-part signal mechanism (Normative)

Every data-quality event produces both:

1. **A per-entity marker** stored in the entity's `attributes` map under the
   reserved `llmobs.dq.*` namespace, so the affected entity is self-describing
   and the condition is visible through the Query API.
2. **A kernel counter** — a telemetry metric the kernel increments per
   occurrence, labeled with at least `project_id` and the signal-specific
   dimension (e.g. field name) — so operators can monitor data quality in
   aggregate.

Additionally, when a value is **replaced** (coerced/normalized/frozen), the
**original offered value** MUST be preserved under the reserved `llmobs.raw.*`
namespace so nothing is lost (invariant 6).

The `llmobs.*` namespace is kernel-owned; normalizers and plugins MUST NOT write
arbitrary keys under it ([Span](#3-span) §6).

### 2. Dimension sanitization (Normative) — LM-11

`environment` MUST be sanitized by the following algorithm, executed **once**, in
the shared `normalize` middleware stage, for **every** transport including compat
plugins (so the value is stored identically regardless of wire format):

1. Trim surrounding whitespace; lowercase.
2. Strip a leading reserved brand prefix if present (the brand constant and its
   `-`-suffixed form; the reserved prefix is defined by `packages/brand` /
   `kernel/pkg/brand`, not hardcoded here).
3. Replace any character outside `[a-z0-9._-]` with `-`.
4. Truncate to a maximum of **40** characters.
5. If the result is empty (or the input was absent), the value **coerces to
   `"default"`**.

Coercion and normalization MUST NOT be silent:

- If the sanitized value differs from the offered value (including coercion to
  `"default"`), the kernel MUST preserve the offered value under
  `llmobs.raw.environment` and increment `llmobs.dq.dimension_coerced` (labeled
  `field=environment`), and set the per-entity marker
  `llmobs.dq.dimension_coerced.environment = true`.
- Because `environment` is **frozen** ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm) §5), sanitization
  happens **before** the frozen-field comparison, so the comparison is between
  sanitized values.

> Evidence: Langfuse sanitizes `environment` (lowercase, reserved-`langfuse`-prefix
> strip, 40-char cap) but does so with a silent `.catch()`→"default" and, critically,
> only on its legacy path — its OTLP path performs **no** normalization, so the same
> value is stored differently by transport (study Ch. 05 §8; Ch. 08). LM-11 fixes both
> defects: one algorithm in the shared normalize stage for all transports, and no
> silent coercion.

### 3. Signal catalog (Normative)

| Signal (`llmobs.dq.*`) | Raised when | Per-entity marker | Preserved under `llmobs.raw.*` | Counter label |
|---|---|---|---|---|
| `truncated` | An adapter truncates an oversized `input`/`output` ([Span](#3-span) §7.2). | `llmobs.dq.truncated = true` | — (truncated content is lost by definition; the media path avoids this) | `field` (`input`/`output`) |
| `truncated_attributes` | An adapter truncates an oversized `attributes` value beyond the per-value size cap ([Span](#3-span) §6.2; default 16 KB, configurable). | `llmobs.dq.truncated_attributes = true` | — (large payloads SHOULD use media references instead) | `key` (the attribute key) |
| `frozen_field_conflict` | A later event offers a different value for a frozen field ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm) §5). | `llmobs.dq.frozen_field_conflict.<field> = <count>` | `llmobs.raw.<field>` = offered value | `field` |
| `dimension_coerced` | A dimension value was normalized/coerced (§2). | `llmobs.dq.dimension_coerced.<field> = true` | `llmobs.raw.<field>` = offered value | `field` |
| `start_time_normalized` | An update offered a `start_time` differing from the frozen original ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm) §5; a specialization of `frozen_field_conflict` for the timing anchor, LM-6). | recorded as `frozen_field_conflict.start_time` | `llmobs.raw.start_time` | `field=start_time` |
| `unmapped_kind` | A source type had no mapping and fell back to `span` ([Span](#3-span) §2.2). | `llmobs.dq.unmapped_kind = true` | `raw_kind` already carries the source value | `raw_kind` |
| `validation_rejected` | A score failed config validation ([Score & ScoreConfig](#5-score-and-scoreconfig) §7). | — (the entity is rejected, not stored) | — | `entity=score`, `reason` |
| `redacted` | Built-in redaction scrubbed one or more PII/secret matches from a payload field before persist (redact stage). | `llmobs.dq.redacted = { total, <rule>: <count>, … }` | — (redacted content is replaced by a token, e.g. `[REDACTED:email]`; the original is intentionally not preserved) | `rule` (the detector name) |
| `clock_skew` | Producer `event_ts` deviated from receive time beyond the configured threshold (ADR-0022). | `llmobs.dq.clock_skew = <delta_seconds>` | — | — |
| `incomplete_trace` | A span references a parent absent from the trace (upstream tail-sampling); computed at trace synthesis. | `llmobs.dq.incomplete_trace = true` (on the synthesized trace) | — | — |

Adding a new signal is additive. A consumer encountering an unknown
`llmobs.dq.*` key MUST ignore it (forward compatibility).

### 4. Reserved key reference (Normative)

| Reserved key | Meaning |
|---|---|
| `llmobs.raw.environment` | Offered environment before sanitization/coercion. |
| `llmobs.raw.start_time` | Offered start_time that conflicted with the frozen value. |
| `llmobs.raw.kind` | Offered kind that conflicted with the frozen value. |
| `llmobs.raw.level` | Source severity/level demoted from `status` ([Span](#3-span) §4.1). |
| `llmobs.raw.<field>` | Generic: any offered value replaced by a frozen/coerced value. |
| `llmobs.dq.<signal>[.<dimension>]` | Per-entity data-quality marker (§3). |

> Evidence: Langfuse's resilient-ingestion choices are consistently *silent* —
> environment `.catch()`→default, async score-validation drops, once-only field
> truncation with no marker (study Ch. 05 §8; Ch. 07 §4; Ch. 04 §10). This chapter
> exists specifically to make every such action observable, which is the recurring
> divergence the study's digest flags (digest §11, "silent coercion is the
> cautionary case").

---

## 10. Adapter guidance

**Section type:** **Informative, except §0.** With the sole exception of the
requirements explicitly marked **Normative** in §0, nothing in this file is
normative: the rest records non-binding implementation guidance for the storage
adapters, derived from the Langfuse study. Where a section is informative,
adapters MAY diverge; those parts are revisitable without a spec change, because
they describe physical choices that the logical model ([Overview](#1-overview)–[Data-quality](#9-data-quality-signals)) forbids from
being observable ([Overview](#1-overview) §1.1). §0's requirements are the exception:
they are load-bearing cross-adapter invariants and are conformance-gated.

### 0. Normative requirements (exception to this file's Informative status)

The requirement below is **Normative** (ADR-0026), notwithstanding this file's
otherwise-informative status. It is a load-bearing cross-adapter invariant that a
physical layout choice can silently violate, so it is stated as a rule and is
conformance-gated. (RULING-CH9's per-query resource-limit requirement is the second
such normative item; it lands with the scale read path in L2.)

#### N1 — RMT-style physical collapse orders by write-arrival, not `event_ts`

The logical merge fold ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm)) is ordered by `event_ts`: that is
the spec semantics for which event wins a field group *inside the fold*. A physical
adapter that keeps one settled row per `(project_id, id)` by **collapsing** duplicate
physical rows — any `ReplacingMergeTree`-style engine — MUST order that collapse by
**write-arrival order** (a monotonic per-write stamp), NOT by `event_ts`.

Rationale: merge-on-write folds each incoming event into the current settled row, so
the **last write already incorporates every prior event** (via per-field provenance).
Write-order is therefore completeness-order. If the physical collapse instead keeps
the max-`event_ts` row, an out-of-order **older** event — whose freshly-written row
correctly folds in all newer data — loses the collapse to a stale higher-`event_ts`
row, and its contribution is **silently dropped**. The physical layer's job is to keep
the already-folded row, never to re-derive the fold from a version column.

This is a real trap, hit and fixed in the ClickHouse adapter (L1): the version column
of a `ReplacingMergeTree` *looks* like it should be `event_ts`, and it must not be.
Use a write-arrival stamp (`ver`) as the engine version and dedup reads by it;
`event_ts` stays a data column used only for spec-level ordering *inside* the fold.
The ClickHouse adapter will not be the last RMT-style adapter — this warning is for
the next author.

### 1. Why this is separate (Informative)

The logical model is storage-neutral. The Query API MUST behave identically over
the lite (Postgres-only) and scale (ClickHouse) adapters. Physical layout —
partitioning, sort keys, indexes, materialized views — is therefore an adapter
concern and is kept out of the normative text so that changing it never breaks a
contract.

### 2. Scale adapter (ClickHouse) — layout guidance (Informative) — LM-9

#### 2.1 Sort keys

- **`project_id` leads every sort key.** Tenant isolation becomes a prefix-range
  read and per-project deletion/retention becomes index-aligned. No
  organization identifier is stored ([Entities & identity](#2-entities-and-identity) §1).
- **Spans (tree-first ordering):** `ORDER BY (project_id, toDate(start_time),
  trace_id, id)`. Putting `trace_id` ahead of `id` clusters a trace's spans
  contiguously, optimizing the dominant "fetch the whole trace tree" read.
- **`kind` as a skip-indexed low-cardinality column, NOT in the sort key.** This
  deliberately diverges from Langfuse (below): keeping `kind` out of the primary
  sort key preserves tree-first locality; kind-scoped analytics ("all
  generations") are served by a skip index / low-cardinality scan or a
  projection (§2.3).
- **Traces:** `ORDER BY (project_id, toDate(start_time), id)`.
- **Scores:** `ORDER BY (project_id, toDate(timestamp), subject_type, id)` — with
  `subject_type` clustering scores by what they attach to.

> Evidence: Langfuse leads every fact table with `project_id` and partitions by
> month, but orders observations `(project_id, type, toDate(start_time), id)` —
> `type` (its `kind`) sits *second*, which optimizes type-scoped analytics at the
> cost of per-trace tree fetches that must fall back to the `trace_id` bloom index
> (study Ch. 03; Ch. 06 §2, §6; digest §9). This guidance keeps `project_id`-first and
> month partitioning but moves `trace_id` ahead of `kind` in the key, because the
> canonical model's dominant read is the trace tree; `kind` filtering is served by a
> skip index. This is an adapter trade-off, explicitly revisitable.

#### 2.2 Partitioning and retention

- **Partition by month** (`toYYYYMM(start_time)` / `toYYYYMM(timestamp)`), so
  time-range queries prune partitions and retention drops whole partitions.
- Implement the merge fold ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm)) with a `ReplacingMergeTree`
  versioned on a **write-arrival stamp `ver` (§0 N1), NOT `event_ts`** — versioning
  on `event_ts` silently drops out-of-order data — plus `is_deleted` as a normal
  read-filtered column (a revivable field-group, never the RMT physical-delete
  feature). Use read-time deduplication: reads MUST dedup on `(project_id, id)`
  (a hand-rolled `argMax`/`LIMIT 1 BY project_id, id` by `ver`, or `FINAL`), because
  the logical idempotency key is `(project_id, id)` alone — **do not** let the
  physical sort key become the dedup identity ([Entities & identity](#2-entities-and-identity) §3.3).

> Evidence: Langfuse's dedup identity is its full sort key, so id reuse across a day
> boundary or a name change leaves duplicate rows that only `LIMIT 1 BY id` reads
> resolve — the source of its date-boundary queue-delay hack (study Ch. 03; Ch. 07
> §6; digest §5, §6). Because this model freezes `start_time` and keys idempotency on
> `(project_id, id)`, the scale adapter avoids the hazard entirely; no date-boundary
> delay is needed (LM-6).

#### 2.3 Projections and materialized views

- Kind-scoped and session-scoped analytics MAY be accelerated with **projections**
  or materialized aggregates as a **future** recovery path if skip-index scans
  prove insufficient. These are invisible to the logical model.
- Session-list acceleration (a materialized `session_id` aggregate) is an adapter
  concern ([Entities & identity](#2-entities-and-identity) §4.1), not a logical entity.

> Evidence: Langfuse built and then *demolished* a whole AggregatingMergeTree
> pre-aggregation layer (`0023`→`0029`); the base ReplacingMergeTree won, and it
> currently ships **no** projections (study Ch. 03; digest, exec summary). Treat
> pre-aggregation as an add-later optimization, not a foundational choice.

### 3. Lite adapter (Postgres) — layout guidance (Informative)

- The merge fold MAY be implemented as an **in-place `UPDATE`** (read-modify-write
  under a row lock or an `INSERT ... ON CONFLICT DO UPDATE`) rather than
  append-only rows, as long as it reproduces the fold in [Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm)
  exactly — including empty-never-clobbers, deep-merge, tags-union, frozen-field
  handling, and tombstones (`is_deleted` as a boolean column).
- Open maps (`attributes`, `usage_details`, …) map naturally onto `jsonb`.
- Promoted dimensions (`environment`, `session_id`, `user_id`) SHOULD be indexed
  b-tree columns; the `attributes` `jsonb` MAY use a GIN index for containment
  queries.
- The lite profile targets the D13 performance envelope (Postgres-only,
  `docker compose up`); it need not match scale-profile analytical throughput,
  only the same observable semantics.
- **Per-field provenance & the field-path separator (adapter-internal).** The
  merge fold tracks, per field-group, the `(event_ts, event_id)` stamp that owns
  it (a `provenance` `jsonb` column) so out-of-order updates converge to the same
  state as the ordered fold. The lite adapter joins field-group path segments
  with an ASCII **SOH (`0x01`)** separator so that a dotted attribute key
  (`gen_ai.request.model`) stays one key while a genuinely nested object
  deep-merges per leaf. This separator is **adapter-internal** and never
  observable: it is legal in `jsonb` (unlike NUL, which raises `22P05`), and
  because §6.3 forbids control characters in attribute keys at normalize time,
  the separator can never collide with a real key. A different adapter MAY choose
  any encoding — the observable fold is what conformance checks.

### 4. Cross-adapter conformance (Informative)

The meta-decision behind this model: the conformance suite (`tools/conformance`)
will drive identical event sequences into both adapters and assert identical
Query-API results, using the merge test vectors ([Update semantics](#6-update-semantics-idempotency-and-the-merge-algorithm) §6) as
the core cases. Any place where an adapter's physical choice becomes observable is
a conformance failure, not a spec change.
