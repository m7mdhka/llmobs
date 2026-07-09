# Query DSL — Normative Spec (`v1alpha1`)

**Status:** `v1alpha1` (maturity per `VERSIONING.md`; additive-only within a
major). **Section type:** Normative except where marked *Informative*. RFC 2119
keywords apply. See ADR-0019 for the decisions (QD-1…QD-10) and rejected
alternatives.

The Query DSL is the **only** read path over the canonical model
(`api/model/v1alpha1/`). There is no raw-SQL escape hatch (D9). A query is one
JSON document; the kernel validates it against `dsl.schema.json` and the field
registry `fields.json`, compiles it per storage adapter, and returns the response
envelope (§9). Both adapters MUST return identical results (the model's
storage-neutral obligation).

## 1. Query shape (Normative) — QD-1

A query is a single JSON object:

```
{
  "version":      "v1alpha1",        // required; the DSL maturity version
  "target":       "spans",           // required; one of: spans | traces | scores
  "timeRange":    { ... },           // required (§6) — every query is time-bounded
  "filters":      [ ... ],           // optional; implicit AND (§3)
  "scores":       [ ... ],           // optional; ONLY on target=traces (§8, semi-join)
  "groupBy":      [ ... ],           // optional (§5)
  "aggregations": [ ... ],           // optional (§5)
  "orderBy":      [ ... ],           // optional (§7); ignored when aggregations present
  "limit":        1000,              // optional (§7); default & max = MAX_LIMIT
  "cursor":       "…"                // optional (§7); opaque keyset cursor
}
```

- `target` ∈ {`spans`, `traces`, `scores`} in `v1alpha1`. A plugin may only
  target what its `query` capability grants (§10).
- A query is either a **row query** (returns entities; no `aggregations`) or an
  **aggregation query** (has `aggregations`, optionally `groupBy`). `orderBy`,
  `limit`, and `cursor` apply to row queries; `groupBy`/`aggregations` define
  aggregation queries. A query MUST NOT combine `cursor` with `aggregations`.
- Unknown top-level keys MUST be rejected (`schema_invalid`, §11).

## 2. Field classes and operators (Normative) — QD-2

Every queryable field belongs to a **class** (declared in `fields.json`).
Operators are defined **per class**, never per field. A condition naming a field
MUST use an operator allowed for that field's class; otherwise `operator_not_allowed` (§11).

| Class | Allowed operators | Value shape |
|---|---|---|
| `string` | `eq, neq, in, not_in, contains, starts_with, is_null` | string; `in`/`not_in` → array of strings |
| `enum` | `eq, neq, in, not_in` | string (a defined enum member) |
| `numeric` | `eq, neq, gt, gte, lt, lte, in, is_null` | number (integer) |
| `decimal` | `eq, neq, gt, gte, lt, lte, in, is_null` | number |
| `timestamp` | `eq, neq, gt, gte, lt, lte, in, is_null` | RFC 3339 string |
| `boolean` | `eq` | boolean |
| `string_array` | `contains_any, contains_all` | array of strings |
| `attr_map` | key-scoped (below) | per key |
| `reference` | reference-equality (below) | object |

### 2.1 Condition forms

A **simple condition** (classes `string`/`enum`/`numeric`/`decimal`/`timestamp`/
`boolean`/`string_array`):

```
{ "field": "<name>", "op": "<operator>", "value": <value> }
```

`is_null` takes no `value` (or `value:true`); it matches rows where the field is
absent/null.

A **map condition** (class `attr_map` — the `attributes` map, and the
`usage_details` / `cost_details` maps) is **key-scoped**:

```
{ "field": "attributes", "key": "<attribute key>", "op": "<op>", "value": <value> }
```

- For `attributes`: values are **string-typed**; ops: `eq, neq, contains,
  exists`. (`exists` takes no value.)
- For `usage_details` / `cost_details` / `provided_usage_details` /
  `provided_cost_details`: values are **numeric**; ops: `eq, neq, gt, gte, lt,
  lte, exists`.
- `contains` is string-only; the numeric comparison ops are numeric-map-only.
  Using a disallowed op for the map's value type is `operator_not_allowed` (§11).

A **reference condition** (class `reference` — e.g. `prompt_ref`,
`pricing_snapshot_ref`, and `config_ref` on scores) is equality on the reference
tuple (this is ruling **Q6** made concrete — it is how filter-by-prompt works):

```
{ "field": "prompt_ref", "op": "ref_eq",
  "value": { "ref_type": "prompt", "ref_id": "…", "ref_label": "…" } }
```

- `ref_type` and `ref_id` are REQUIRED in the value; `ref_label` is OPTIONAL.
  Matching is equality on the provided sub-fields (a reference matches when its
  `ref_type` and `ref_id` — and `ref_label` when supplied — are equal).
- Every reference field of every future plugin-owned reference type is queryable
  by this same mechanism, uniformly.

### 2.2 Negation and NULL (Normative)

**Negations match unset (NULL) rows.** `neq`, `not_in`, and negated reference
equality select rows where the field is **unset** as well as rows whose value
differs. Worked example: `{"field":"user_id","op":"neq","value":"bob"}` returns
spans with no `user_id` — a span with no user *is* not bob. Likewise
`{"field":"kind","op":"not_in","value":["generation"]}` returns rows whose `kind`
is anything other than `generation`, **including** rows where `kind` is unset.

This is **observable Query API semantics**: every adapter's compiler MUST
reproduce it (e.g. Postgres compiles `neq` as `IS DISTINCT FROM` and `not_in` as
`col IS NULL OR col <> ALL(...)`; the ClickHouse compiler MUST match). It is part
of the compiler-interface conformance expectations. `is_null` remains the
explicit "field is unset" test.

## 3. Boolean structure — deliberately shallow (Normative) — QD-3

- `filters` is an array; its members are combined with an **implicit AND**.
- A member MAY instead be an **OR group**: `{ "any": [ <condition>, … ] }`,
  whose members are combined with OR.
- **Maximum nesting depth is `MAX_NESTING_DEPTH` (= 2): an AND of ORs.** An OR
  group MUST contain only simple/map/reference conditions — it MUST NOT contain
  another `any` group. Arbitrary boolean trees are not expressible in
  `v1alpha1`; deeper nesting is a future additive change (D11).

This bounds query cost and keeps the per-adapter compiler simple.

## 4. Targets and their fields (Normative)

The queryable fields per target are the promoted/first-class fields of the
corresponding entity, enumerated in `fields.json` (which MUST match
`api/model/v1alpha1/02-span.md`, `03-trace.md`, `04-score.md`). Only fields
listed there are queryable; a condition, `groupBy`, `orderBy`, or aggregation
naming any other field MUST be rejected `unknown_field` (§11).

- `attributes` (all targets), `usage_details`/`cost_details` and their
  `provided_*` twins (target `spans`) are `attr_map` fields, queryable only via
  key-scoped map conditions (§2.1).
- Non-promoted span fields (`input`, `output`, `events`, `model_parameters`,
  `input_content_type`, …) are **not** queryable (they are not in the promoted
  set); they are returned in row results subject to redaction (§10).

## 5. Aggregations and grouping (Normative) — QD-4

An aggregation query carries `aggregations` and optionally `groupBy`.

- **`aggregations`**: an array of `{ "op": <agg>, "field": <name>, "key": <k>?,
  "alias": <name>? }`. `<agg>` ∈ `count, count_distinct, sum, avg, min, max,
  p50, p90, p95, p99`.
  - `count` MAY omit `field` (counts rows).
  - `sum, avg, min, max, p50, p90, p95, p99` require a `numeric`/`decimal`
    promoted field, **or** a numeric map key (`usage_details`/`cost_details`
    with `key`).
  - `count_distinct` requires a promoted field.
  - `alias` names the output column; defaults to `<op>_<field>`.
  - At most `MAX_AGGREGATIONS` (= 10) aggregations per query.
- **`groupBy`**: an array (≤ `MAX_GROUPBY` = 3) whose members are either a
  promoted `enum`/`string` field name, or **one** time bucket:
  `{ "field": "start_time" | "timestamp", "interval": "1m" | "5m" | "1h" | "1d" }`.
  - At most one time-bucket member per query. The bucket `field` MUST be the
    target's time anchor (`start_time` for spans, `start_time`/`timestamp` for
    traces, `timestamp` for scores).
  - Grouping by a high-cardinality field (`id`, `trace_id`) is rejected
    `not_groupable` (§11).
- An aggregation query returns group rows; `orderBy`/`cursor` MUST NOT be
  present (results are bounded by the group cardinality and `timeRange`).

## 6. Bounded scans — `timeRange` is mandatory (Normative) — QD-5

Every query MUST carry `timeRange`:

```
{ "from": "<RFC3339>", "to": "<RFC3339>" }
```

- `from` < `to`; both REQUIRED.
- The window `to − from` MUST NOT exceed the per-project maximum,
  `LLMOBS_QUERY_MAX_WINDOW` (kernel configuration). A larger window is rejected
  `ceiling_exceeded` (§11).
- `timeRange` filters on the target's time anchor (`start_time` for spans and
  traces, `timestamp` for scores). This is a spec-level requirement, not an
  adapter detail: it bounds every scan and makes query cost predictable.

## 7. Ordering and pagination — keyset only (Normative) — QD-6

- **`orderBy`**: an array of `{ "field": <name>, "dir": "asc" | "desc" }`. Fields
  MUST be **orderable** (marked `orderable: true` in `fields.json` — i.e.
  promoted/indexed). The default order is `[{start_time|timestamp, desc}, {id,
  asc}]` (the target's time anchor then `id`). `id` is always appended as the
  final tiebreak to guarantee a total order for keyset paging.
- **Pagination is keyset only.** There is **no offset pagination** in this API,
  ever. `cursor` is an **opaque base64** token encoding the ordering key of the
  last returned row. A client passes back the `cursor` from the previous
  response (§9) to fetch the next page; the query (target, filters, timeRange,
  orderBy) MUST be identical across a cursor sequence, else `schema_invalid`
  (§11); a cursor that does not decode is likewise `schema_invalid`.
- `limit` bounds page size; default and maximum = `MAX_LIMIT` (= 1000).

## 8. Score semi-join on `traces` (Normative) — QD-9

A `traces` query MAY carry a `scores` block — an array of score conditions —
with **semi-join** semantics: it selects **traces that have at least one score
matching all listed conditions**. This makes "traces where hallucination < 0.5"
one query, not two.

```
"scores": [ { "name": "hallucination", "data_type": "numeric",
              "op": "lt", "value": 0.5, "source": "llm_judge" } ]
```

- Each score condition: `{ "name" (required), "data_type" (**required**), "op",
  "value", "source"? }`. `data_type` is REQUIRED — there is **no inference and no
  coercion, ever**.
- **`data_type` fully determines the allowed operators, the matched column, and
  the JSON type of `value`:**

  | `data_type` | allowed `op` | matched column | `value` JSON type |
  |---|---|---|---|
  | `numeric` | `eq, neq, gt, gte, lt, lte` | `value_numeric` (LM-3) | number |
  | `categorical` | `eq, neq, in` | `value_string` | string (for `in`: array of strings, ≤ `MAX_IN_LIST`) |
  | `boolean` | `eq` | `value_numeric` (`0`/`1`) | boolean (`true`→1, `false`→0) |

  Any mismatch — an operator not allowed for the `data_type`, or a `value` whose
  JSON type is wrong for the `data_type` — is a **`score_type_mismatch`** error
  (422, §11). The kernel MUST NOT coerce (e.g. a string `"0.5"` for a `numeric`
  score is rejected, not parsed).
- `source` (optional) restricts to scores of that `source` (§`04-score.md` §4).
- Multiple entries in `scores` are ANDed at the **trace** level: a trace
  qualifies if, for **each** entry, it has ≥1 matching score (the matches need
  not be the same score).
- The `scores` block is valid **only** on `target=traces`; present on any other
  target ⇒ `schema_invalid` (400, §11).

## 9. Response envelope (Normative) — QD-8

Every response is:

```
{
  "version": "v1alpha1",
  "data":     [ … ],                 // rows (entities) or aggregation group rows
  "cursor":   "…",                   // present iff more row-query results exist
  "stats":    { "elapsed_ms": 0, "scanned": 0, "returned": 0 },
  "warnings": [ { "code": "…", "message": "…", "detail": { … } } ]
}
```

- `data`: for a row query, canonical entities (span/trace/score shapes from
  `api/model/v1alpha1/schema/`, subject to field redaction §10); for an
  aggregation query, one object per group (`groupBy` values + aggregation
  aliases).
- `cursor`: present on a row query iff a next page exists; absent otherwise.
  Aggregation queries never return a `cursor`.
- `stats`: at minimum `elapsed_ms`; adapters MAY add fields.
- `warnings`: non-fatal signals — data-quality and partial-result notices —
  using the `llmobs.dq.*` conventions from `08-data-quality.md` (e.g. a page that
  included truncated payloads carries a warning referencing
  `llmobs.dq.truncated`). A partial result (e.g. a scan cut short by an adapter
  guard) MUST emit a warning; it MUST NOT silently drop data.

### 9.1 Bad data never fails a valid query (Normative)

**A stored row MUST NEVER be able to turn someone else's valid query into a
`500`.** This is an invariant, not a nicety: query validity depends only on the
query, never on the data it scans.

Concretely, when a condition cannot be evaluated against a row because the
**stored value has the wrong type** — e.g. a numeric-map condition
(`usage_details.input > 100`) meets a row whose `usage_details.input` is not a
number — the row **does not match** (it is excluded from results); the adapter
MUST NOT raise an error. The response MAY carry an `llmobs.dq.*` warning so the
caller can tell "no matches" from "some rows had malformed data". This is **not**
a `422`: the *query* is valid; the *data* is bad, and a plugin must be able to
distinguish the two. (Adapters implement this by guarding the cast, e.g.
`jsonb_typeof(...) = 'number'` before `::numeric`.)

The envelope is versioned; fields are additive within a major.

## 10. Permissions and field-level redaction (Normative)

Access is the **intersection** of the plugin's service-token capability and the
forwarded user assertion (invariant 7); the Query API computes it and never
trusts a plugin-supplied identity.

- The **`query`** capability grants read access. It is scoped to targets:
  `query:spans`, `query:traces`, `query:scores`. A query whose `target` is not
  granted ⇒ `unauthorized` (§11).
- **Payload-granularity scopes** project fields out of row results
  (field-level redaction):
  - `traces:read.metadata` — grants the promoted/dimension fields and
    aggregations, but **not** payload fields. In row results, `input`, `output`,
    `events`, `attributes` values, and `model_parameters` are **omitted** (the
    row is returned with those fields absent).
  - `traces:read.payloads` — additionally grants the payload fields
    (`input`/`output`/`events`/`attributes`/`model_parameters`).
  - The same split applies to `spans` (`spans:read.metadata` /
    `spans:read.payloads`) and to score `metadata`/`comment`.
- Redaction is applied by the Query API **after** the adapter returns rows, so a
  metadata-only caller can still **filter and aggregate** on promoted fields but
  never receives payloads. A query that filters on a field the caller may read
  but requests payloads it may not read succeeds with payloads omitted (not an
  error); a query that filters on a payload field is not possible because payload
  fields are not queryable (§4).
- Field-level redaction is part of **this contract**, not an adapter concern, so
  it is uniform across lite and scale.

## 11. Error taxonomy (Normative)

Errors carry a machine-readable **`error.code`** (a closed enum, mirrored in the
OpenAPI) and an HTTP status. The split is sharp:

- **400 `schema_invalid`** — the document fails `dsl.schema.json`: malformed,
  unknown key, wrong operator token for a class, nesting > `MAX_NESTING_DEPTH`,
  `scores` on a non-traces target, `cursor` with `aggregations`, an `in`-list or
  array over its schema `maxItems`, `limit` over its schema `maximum`, etc.
  These are the errors the schema alone catches.
- **422 (structurally valid, semantically rejected)** — the document passes
  `dsl.schema.json` but violates a rule the schema cannot express. These require
  the field registry (`fields.json`), the true condition count, or per-project
  config.
- **403 `unauthorized`**, **404 `not_found`** — permission / existence.

**`dsl.schema.json` is necessary but not sufficient:** passing it means the
document is well-formed; the 422 checks below still apply. The full `error.code`
enum:

| `error.code` | When | HTTP |
|---|---|---|
| `schema_invalid` | Fails `dsl.schema.json` (§1–§7 structure and schema-expressible ceilings). | 400 |
| `unknown_field` | A condition/order/group/aggregation names a field not in `fields.json` for the target. | 422 |
| `operator_not_allowed` | An operator not permitted for the named field's class (§2), or a map op wrong for the map's value type. | 422 |
| `condition_limit_exceeded` | The **true** total condition count (each OR-group member + each `scores` entry) exceeds `MAX_CONDITIONS` — the schema's `maxItems` on `filters` is only an upper bound. | 422 |
| `ceiling_exceeded` | A non-schema ceiling: `timeRange` window > `LLMOBS_QUERY_MAX_WINDOW` (per-project config the schema cannot know). | 422 |
| `score_type_mismatch` | A `scoreCondition`'s `op` or `value` JSON type is wrong for its `data_type` (§8). No coercion. | 422 |
| `not_orderable` / `not_groupable` | `orderBy`/`groupBy` names a field not marked orderable/groupable in `fields.json`. | 422 |
| `unauthorized` | The caller's effective permission does not grant the `target`. | 403 |
| `not_found` | Single-entity fetch: no such entity in the caller's project scope. | 404 |

An adapter MUST reject an invalid or over-ceiling query **before** scanning.
Error responses carry `{ code, message, detail? }`; `detail` names the offending
field/constant.

## 12. Contract constants (Normative) — QD-7

These are **versioned contract constants**: plugins may rely on them, and the
gateway/Query API enforces them uniformly (they also feed per-plugin quotas). A
query violating any of these is rejected before execution (§11: `schema_invalid`
for the schema-expressible ceilings, `condition_limit_exceeded` for the true
condition count, `ceiling_exceeded` for the time-window).

| Constant | Value | Meaning |
|---|---|---|
| `MAX_CONDITIONS` | 32 | Total conditions across `filters` (counting each OR-group member) plus `scores`. |
| `MAX_LIMIT` | 1000 | Maximum (and default) row-query page size. |
| `MAX_IN_LIST` | 256 | Maximum elements in an `in` / `not_in` / `contains_any` / `contains_all` array. |
| `MAX_GROUPBY` | 3 | Maximum `groupBy` members. |
| `MAX_AGGREGATIONS` | 10 | Maximum `aggregations` per query. |
| `MAX_NESTING_DEPTH` | 2 | Maximum boolean nesting (AND of ORs). |
| `LLMOBS_QUERY_MAX_WINDOW` | per-project config | Maximum `timeRange` window; enforced by the kernel. |

Changing a constant's value, or adding a constant, is an additive contract change
recorded per `VERSIONING.md`. Constants are surfaced to clients (e.g. via a
capabilities endpoint) so plugins can self-limit.

## 13. Non-DSL endpoints (Informative)

The following are defined in `api/openapi/v1alpha1/query.yaml`, not the DSL, and
are summarized here for context (QD-10):

- `GET /v1alpha1/traces/{trace_id}/tree` — the optimized full span-tree fetch
  (the reason for the tree-first sort key), returning the trace, its spans in
  tree order, and span events, with the merge/dedup semantics of LM-5.
- `GET /v1alpha1/spans/{id}`, `/traces/{id}`, `/scores/{id}` — single-entity
  fetches by id.
- `POST /v1alpha1/scores` — the score **write** path for plugins holding the
  `write` capability, enforcing LM-3/LM-8 (measurement-only value model,
  `(subject_type, subject_id)` subjects including plugin-namespaced types).

Ingestion (OTLP) is a different surface and is not part of this contract.
