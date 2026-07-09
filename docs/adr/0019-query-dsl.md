# ADR-0019: Query DSL

- **Status:** Accepted
- **Date:** 2026-07-09
- **Deciders:** m7mdhka (query API design session)
- **Relates to:** ADR-0009 (typed query DSL, compiled per adapter — no raw SQL, D9), ADR-0016 (canonical model)

## Context

The canonical data model (ADR-0016) is the storage-neutral logical model; the
**Query API** is the single typed read path over it. Plugins query only through
this API (invariants 2–3); there is no raw-SQL escape hatch (D9). The DSL must be
expressive enough for the real workloads (eval filtering, cost dashboards, trace
exploration) yet bounded enough that a per-adapter compiler (Postgres for lite,
ClickHouse for scale) can implement it without ambiguity and that query cost is
predictable. Langfuse's filter-DSL-to-SQL compiler and its Cube.js-like semantic
layer are the prior art (study Ch. 12): a structured filter vocabulary resolved
through a per-table column catalogue into parameterized SQL, plus a closed
dimension/measure surface with a hard row cap and a high-cardinality denylist.

## Decision

Adopt the query DSL **`v1alpha1`**, specified normatively in
[`api/query/v1alpha1/`](../../api/query/v1alpha1/). The ten decisions:

| # | Decision | Spec |
|---|---|---|
| QD-1 | One JSON document per query: `{version, target, filters, scores?, groupBy?, aggregations?, timeRange, orderBy?, limit?, cursor?}`; targets `spans`/`traces`/`scores`; the only read path. | `00-dsl-spec.md` §1 |
| QD-2 | Field **classes** with operators defined per class (string/enum/numeric/decimal/timestamp/boolean/string_array/attr_map/reference); reference-equality is the generic filter-by-prompt mechanism (Q6). | §2 |
| QD-3 | Deliberately shallow booleans: `filters` is an implicit AND; a member may be an `{any:[…]}` OR group; **max nesting depth 2** (AND of ORs). | §3 |
| QD-4 | Aggregations (`count`/`count_distinct`/`sum`/`avg`/`min`/`max`/`p50`/`p90`/`p95`/`p99`) over numeric fields + map keys; `groupBy` over enum/string fields and/or one time bucket; ≤ 3 groupBy, ≤ 10 aggregations. | §5 |
| QD-5 | `timeRange` is **mandatory** on every query; window capped per project (`LLMOBS_QUERY_MAX_WINDOW`). | §6 |
| QD-6 | **Keyset pagination only**, opaque base64 cursor; no offset pagination, ever; `orderBy` restricted to promoted/indexed fields; default `(time anchor desc, id asc)`. | §7 |
| QD-7 | Contract ceilings as versioned named constants: ≤ 32 conditions, ≤ 1000 limit, ≤ 256 `in`-list, plus the QD-4 limits. | §12 |
| QD-8 | Response envelope `{version, data, cursor?, stats, warnings}`; `warnings` carries data-quality/partial-result signals via `llmobs.dq.*`. | §9 |
| QD-9 | Score **semi-join** on `traces`: a `scores` block selects traces having ≥1 matching score, matched against the authoritative value column per `data_type` (LM-3). | §8 |
| QD-10 | Non-DSL endpoints in OpenAPI: trace-tree fetch, single-entity fetches, and `POST /scores` write path (LM-3/LM-8). Ingestion is a different surface. | `api/openapi/v1alpha1/query.yaml` |

The DSL is compiled per storage adapter and MUST return identical results from
both (the model's storage-neutral obligation). Field-level redaction
(`*:read.metadata` vs `*:read.payloads`) and the permission intersection
(invariant 7) are part of this contract (`00-dsl-spec.md` §10), not adapter
concerns.

## Consequences

**Positive.**
- A single, versioned, bounded read contract that both adapters and every plugin
  build against; query cost is predictable (mandatory time bound + ceilings +
  keyset paging + shallow booleans).
- Reference-equality (QD-2/Q6) restores Langfuse's filter-by-prompt generically,
  for every plugin-owned reference, without promoting plugin fields.
- The eval-first killer query (traces filtered by score, QD-9) is one query.

**Negative / costs.**
- Expressiveness is deliberately capped: no arbitrary boolean trees, no offset
  paging, a fixed operator/aggregation set. Deeper needs are additive future
  changes (D11), not escape hatches.
- The per-class operator↔field matching and the exact `MAX_CONDITIONS` total (which
  counts OR-group members) are enforced by the kernel against `fields.json`, not
  by the JSON Schema alone — so the registry and the schema must be kept in sync
  (a CI consistency check covers `fields.json` ↔ model).

## Alternatives considered

- **Raw SQL / a SQL-subset escape hatch.** Rejected — violates D9 and invariants
  2–3 (plugins are untrusted; no raw store access), and makes cross-adapter
  equivalence and cost bounding impossible.
- **Arbitrary boolean nesting.** Rejected — unbounded compiler complexity and
  query cost; QD-3 caps at AND-of-ORs, which covers the observed workloads.
- **Offset pagination.** Rejected — O(n) deep-page cost and correctness drift
  under concurrent writes; keyset-only (QD-6) is correct and bounded.
- **Per-field operator definitions.** Rejected in favor of per-class operators
  (QD-2) — fewer moving parts, and the class is the natural unit (Langfuse's
  filter vocabulary is likewise operator-per-type, study Ch. 12).

## Links

- Spec: [`api/query/v1alpha1/`](../../api/query/v1alpha1/) (`00-dsl-spec.md`,
  `dsl.schema.json`, `fields.json`, `examples/`).
- OpenAPI: [`api/openapi/v1alpha1/query.yaml`](../../api/openapi/v1alpha1/query.yaml).
- Evidence: study Ch. 12 (Langfuse filter-DSL→SQL compilation and semantic layer)
  — prior art for compile-per-adapter and the closed dimension/measure surface.
- Relates to: ADR-0009 (D9), ADR-0016 (canonical model), D11 (additive versioning).
