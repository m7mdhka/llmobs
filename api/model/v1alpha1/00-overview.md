# Canonical Data Model — Overview (`v1alpha1`)

**Status:** `v1alpha1` (maturity per [`VERSIONING.md`](../../../VERSIONING.md); additive-only
within a major, breaking changes require an ADR + deprecation window).
**Section type:** this file is **Normative** except where a block is marked
*Informative*.

> This specification is the storage-neutral **logical** data model of LLMObs. It
> is the model every ingestion normalizer maps *into*, the Query API reads
> *from*, and every plugin consumes. It is a **contract**, authored before any
> code (invariant 5). It contains no Go, no TypeScript, no SQL DDL.

## 1. Scope (Normative)

This model defines the canonical logical entities **Trace**, **Span**, **Score**,
**ScoreConfig**, and **MediaReference**, their fields, identity and idempotency
rules, update/merge semantics, usage/cost representation, cross-reference
conventions, and data-quality conventions.

It deliberately does **not** define: sessions, users, or threads as entities
(§`01-entities.md` §4); datasets, dataset runs, or run items (owned by the evals
plugin); physical storage layout (informative guidance only, §`99-adapter-guidance.md`);
the wire/transport encodings (OTLP, compat endpoints) — those map *into* this
model; or the Query API grammar (a separate contract that reads this model).

### 1.1 Storage neutrality and the conformance obligation (Normative)

The model is **logical**. Two independent adapters — the lite profile
(Postgres-only) and the scale profile (ClickHouse) — MUST produce **identical
Query-API-observable behavior** for the same sequence of ingested events. All
normative text in this specification describes *observable semantics only*. No
requirement here may be satisfied by, or depend on, a particular physical
representation.

Where this document states a rule (a MUST/MUST NOT), the conformance suite
(`tools/conformance`) will assert it against both adapters. The merge test
vectors in §`05-update-semantics.md` are the sharpest instrument of this
obligation and are written as executable data.

## 2. Relationship to OpenTelemetry GenAI (Normative)

OTLP + the OpenTelemetry GenAI semantic conventions are LLMObs's canonical
ingestion format (invariant 6). This model is designed as the **normalized
target** of that format:

- A canonical **Span** corresponds to an OTel span; a canonical **Trace** is the
  root aggregation over spans sharing a `trace_id`.
- Canonical identity (§`01-entities.md` §3) reuses OTel `trace_id` / `span_id`.
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

## 3. Reading guide (Informative)

| File | Type | Contents |
|---|---|---|
| `00-overview.md` | Normative | scope, versioning, OTel relationship, glossary |
| `01-entities.md` | Normative | entity inventory, hierarchy, identity, non-entities |
| `02-span.md` | Normative | span fields, kinds, payload shapes, dimensions, status, media tokens |
| `03-trace.md` | Normative | trace fields, trace↔span merge relationship, tags |
| `04-score.md` | Normative | score value model, sources, configs, subjects, plugin subject registration |
| `05-update-semantics.md` | Normative | merge algorithm, frozen fields, idempotency, **test vectors** |
| `06-usage-cost.md` | Normative | usage/cost maps, well-known keys, derivation precedence, pricing snapshot |
| `07-references.md` | Normative | cross-reference format, label snapshots, dangling tolerance |
| `08-data-quality.md` | Normative | the counter/flag conventions used across the spec |
| `99-adapter-guidance.md` | **Informative** | physical layout guidance (lite vs scale) |
| `schema/` | Normative | JSON Schemas (Draft 2020-12) + valid/invalid examples |
| `validation/` | Informative | per-dialect field-by-field mapping worksheets |

## 4. RFC 2119 keywords (Normative)

The key words **MUST**, **MUST NOT**, **REQUIRED**, **SHALL**, **SHALL NOT**,
**SHOULD**, **SHOULD NOT**, **RECOMMENDED**, **MAY**, and **OPTIONAL** are to be
interpreted as described in RFC 2119 and RFC 8174 when, and only when, they
appear in all capitals.

## 5. Glossary (Normative)

| Term | Definition |
|---|---|
| **Entity** | A canonical object with an identity: Trace, Span, Score, ScoreConfig, MediaReference. |
| **Promoted field** | A first-class, typed, queryable field on an entity (as opposed to a key in the `attributes` map). The promoted set is fixed per major version; additions require an ADR (LM-2). |
| **Attributes map** | The open key/value map holding every non-promoted attribute, including all raw ingested attributes. |
| **Kind** | The closed canonical enum classifying a span's semantic role (`span-002` §2). |
| **Payload shape** | One of three nested field profiles a span may take: `point_event ⊂ timed_span ⊂ generation_shaped` (`span-002` §3). |
| **Dimension** | A promoted grouping field: `environment`, `release`, `version`, `session_id`, `user_id`. |
| **Provided vs resolved** | For usage/cost: `provided_*` is what the client sent; the resolved value is what kernel enrichment computed (§`06-usage-cost.md`). |
| **`event_ts`** | The logical version stamp used to order updates to an entity; latest wins per field-group (§`05-update-semantics.md`). |
| **Tombstone** | A logical delete marker (`is_deleted`) applied by re-inserting the entity's key with a later `event_ts`. |
| **Frozen field** | A field immutable after first write; a later value is rejected/normalized, never applied (§`05-update-semantics.md` §3). |
| **Subject** | The `(subject_type, subject_id)` target a score attaches to (`score-004` §5). |
| **Reference** | A `(type, id)` pointer to another entity or plugin-owned object, optionally carrying a label snapshot (§`07-references.md`). |
| **Data-quality signal** | A counter or boolean flag the model raises when it normalizes, coerces, or truncates input rather than failing (§`08-data-quality.md`). |

## 6. Versioning statement (Normative)

This is `v1alpha1`. Within a maturity version, changes MUST be additive
(new OPTIONAL fields, new kinds, new well-known map keys). Removing, renaming,
retyping, or tightening an existing field is breaking and MUST proceed via a new
maturity version (`v1beta1`, then `v1`) with an ADR and a deprecation window.
Adding a field to the **promoted set** (§`02-span.md` §1) is a model change that
requires an ADR even though it is additive, because promotion is a near-permanent
commitment (§`02-span.md` §1.1).
