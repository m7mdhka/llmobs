# Query DSL examples

Each `NN-*.json` is a query document that MUST validate against
[`../dsl.schema.json`](../dsl.schema.json); each `invalid-*.json` MUST be rejected
by it (CI asserts both). Below: the intent and the expected result shape of every
example.

## Valid

| File | Intent | Expected result shape |
|---|---|---|
| `01-eval-killer-traces-low-hallucination` | The eval-first killer query (QD-9): production traces that have an `llm_judge` `hallucination` score `< 0.5`, newest first. One query, not two. | Row query → `data`: trace entities; `cursor` if > 100 match. |
| `02-cost-by-model-by-day` | Cost dashboard: generation spend and token totals grouped by model and day. | Aggregation query → `data`: one row per `(model, day)` with `cost`, `calls`, `tokens`; no cursor. |
| `03-attributes-string-filter` | Filter on an `attributes` map key (`gen_ai.provider.name = openai`) on generation spans. | Row query → generation span entities. |
| `04-usage-map-numeric-filter` | Numeric map filter: spans whose `usage_details.input > 1000`, ordered by `total_cost` desc. | Row query → span entities, most expensive first. |
| `05-reference-filter-by-prompt` | Filter-by-prompt via the generic reference mechanism (Q6): spans whose `prompt_ref` points at `prompt_greeting`. | Row query → generation span entities. |
| `06-tags-filter` | `string_array` filter: traces tagged with **both** `prod` and `support`. | Row query → trace entities. |
| `07-or-group` | Boolean AND-of-ORs (QD-3): generations/embeddings that are **either** errored **or** in staging. | Row query → span entities. |
| `08-cursor-continuation` | Keyset pagination (QD-6): the next page of a session's spans via an opaque cursor. | Row query → next page of span entities; `cursor` if more. |
| `09-session-grouping` | LM-7 derived sessions expressed in the DSL: a user's traces grouped by `session_id` with first/last-seen. | Aggregation query → one row per session. |
| `10-spans-name-prefix-ordered` | `starts_with` string op + explicit keyset order `(start_time desc, id asc)`. | Row query → span entities in total order. |
| `11-score-percentiles-by-source` | Score analytics: avg/p50/p90 of a numeric score grouped by `source`. | Aggregation query → one row per source with percentile columns. |
| `12-spans-errors-in-window` | Errored spans (excluding guardrails) in the window, newest first. | Row query → span entities. |

## Invalid (each rejected by the schema for the intended reason)

| File | Violates | Intended rejection |
|---|---|---|
| `invalid-01-over-limit-limit` | `MAX_LIMIT` (QD-7) | `limit 5000 > 1000` |
| `invalid-02-in-list-too-large` | `MAX_IN_LIST` (QD-7) | `in`-list has 257 > 256 items |
| `invalid-03-too-many-groupby` | `MAX_GROUPBY` (QD-4) | 4 `groupBy` > 3 |
| `invalid-04-too-many-aggregations` | `MAX_AGGREGATIONS` (QD-4) | 11 aggregations > 10 |
| `invalid-05-nesting-depth` | `MAX_NESTING_DEPTH` (QD-3) | an `any` group nested inside an `any` group |
| `invalid-06-too-many-conditions` | `MAX_CONDITIONS` (QD-7) | 33 `filters` > 32 |
| `invalid-07-scores-on-spans` | QD-9 | `scores` block on a non-`traces` target |
| `invalid-08-cursor-with-aggregations` | QD-1 | `cursor` combined with `aggregations` |
| `invalid-09-unknown-key` | QD-1 | unknown top-level key |
| `invalid-10-missing-timerange` | QD-5 | `timeRange` is mandatory |

Note: two ceilings are enforced by the **kernel against `fields.json`**, not the
schema, so they have no schema-level invalid example here: the *exact*
`MAX_CONDITIONS` total (which counts each OR-group member, not just top-level
`filters` length) and `LLMOBS_QUERY_MAX_WINDOW` (a per-project config value). The
schema's `maxItems: 32` on `filters` is the structural upper bound; the precise
total-condition count and the time-window limit are kernel checks (`over_limit`).
