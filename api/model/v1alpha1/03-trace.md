# Trace (`v1alpha1`)

**Section type:** Normative except where marked *Informative*.

A **Trace** is the root of one end-to-end unit of work. It is an aggregation over
the spans that share its `id` as their `trace_id`. A trace carries a small set of
trace-level promoted fields and dimensions; the detail lives in its spans.

## 1. Field reference (Normative)

| Field | Type | Null? | Frozen? | Semantics |
|---|---|---|---|---|
| `id` | string | no | **yes** | Trace identity; OTel `trace_id` hex where present (`01-entities.md` §3). |
| `project_id` | string | no | **yes** | Tenant scope. |
| `name` | string | yes | no | Human-readable trace name. |
| `start_time` | timestamp | no | **yes** | Trace start; the identity/merge anchor (`05-update-semantics.md`). |
| `end_time` | timestamp | yes | no | Trace end. MAY be derived by an adapter as `max(span.end_time)`; §3. |
| `status` | object | no | no | OTel-aligned `{code, message?}` (`02-span.md` §4.1); defaults `{code:"unset"}`. |
| `input` | opaque | yes | no | Trace-level input (`02-span.md` §4.3); opaque. |
| `output` | opaque | yes | no | Trace-level output; opaque. |
| `tags` | array<string> | no | no | §2. Defaults to `[]`. Union-merged on update. |
| `environment` | string | no | **yes** | Sanitized dimension; defaults `"default"` (`02-span.md` §4.2, `08-data-quality.md` §2). |
| `release` | string | yes | no | Build/deployment identity. |
| `version` | string | yes | no | Per-call logic/prompt version. |
| `session_id` | string | yes | no | Session dimension (`01-entities.md` §4.1). |
| `user_id` | string | yes | no | User dimension. |
| `attributes` | map<string, value> | no | no | Open map; raw attributes preserved (`02-span.md` §6). Defaults `{}`. |
| `total_cost` | decimal \| null | yes | no | **Derived** trace-level cost: `SUM(span.total_cost)` over the trace's **non-aggregate** spans (`06-usage-cost.md` §7.1 — an `agent_step`/`tool_call` span's cost duplicates its child model calls, so it is excluded to avoid double-counting). Null when the trace has no leaf cost. A query-time derived field like `end_time`/`span_count`. Both adapters round the roll-up to a shared fixed decimal scale (Postgres sums exact `NUMERIC`, ClickHouse accumulates `Float64`), so the result is **byte-identical** cross-adapter — conformance-tested, not merely within a tolerance. |

- A trace has **no** `kind` and **no** generation fields — those are span-only.
- Trace `input`/`output`/`status` are independent of any span's; they are set by
  trace-level events or by a root span carrying trace-level updates (§3).

> Evidence: Langfuse's `traces` table carries `id, timestamp, name, user_id,
> metadata, release, version, project_id, environment, public, bookmarked, tags,
> input, output, session_id` (study Ch. 05 §1). This model keeps the equivalent
> promoted/dimension set, drops UI-only flags (`public`, `bookmarked`) to plugin `kv`
> (`01-entities.md` §4.1), and folds `metadata` into the general `attributes` map.

## 2. Tags (Normative)

`tags` is a set of free-form strings modeled as an array with **set semantics**:
order is not significant and duplicates are not observable. On update, `tags` is
**union-merged** — a later event adds tags, it does not replace the set
(§`05-update-semantics.md` §2). There is no tag removal in `v1alpha1`; tag
retraction, if needed, is a future additive capability.

## 3. Trace ↔ span relationship (Normative)

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
merge algorithm as any other update (§`05-update-semantics.md`). A span's own
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

## 4. Identity, freezing, merging (Normative)

Trace identity, frozen fields, and the merge algorithm are defined once in
`05-update-semantics.md` and apply to traces exactly as to spans. Trace frozen
fields: `id`, `project_id`, `start_time`, `environment` (`05-update-semantics.md`
§3). The trace idempotency key is `(project_id, id)` (`01-entities.md` §3.2).
