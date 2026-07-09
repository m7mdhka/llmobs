# Validation — OpenLLMetry (Traceloop)

> **Post-review:** the design review ruled on every finding below; the resolutions applied to the spec are tabulated in [`README.md`](README.md#resolutions-applied-post-review-2026-07-09). This worksheet is preserved as the pre-resolution analysis and fixture seed.

**Dialect:** OpenLLMetry / Traceloop OTLP export conventions
**Target model:** LLMObs canonical `v1alpha1` (`02-span.md`, `03-trace.md`, `06-usage-cost.md`, `08-data-quality.md`)
**Date:** 2026-07-09

Traceloop emits OTel spans but mixes three vocabularies: (a) OTel GenAI semconv (`gen_ai.*`), (b) its own proprietary keys (`traceloop.*`), and (c) legacy/pre-semconv keys (`llm.*`). It also flattens list-valued messages/tool-calls into index-suffixed flat attribute keys (`gen_ai.prompt.0.role`, …) because OTLP attributes cannot hold nested structures. Every divergence from official OTel GenAI is flagged in-line and in the design-bugs section.

---

## 1. Representative agent-shaped trace (Traceloop wire form)

One trace, five spans. Traceloop names each span with its entity name and stamps `traceloop.span.kind`. Shown as `{key = value}` OTLP attribute sets. Resource attributes are shared by all spans.

### Resource attributes (shared, from the OTLP Resource)
```
service.name              = "research-agent"
deployment.environment    = "Production"          # note capitalization
telemetry.sdk.name        = "opentelemetry"
traceloop.sdk.version     = "0.33.12"
```

### Span A — root workflow  (OTLP span_id=aa..)
```
name                      = "research_agent.workflow"
traceloop.span.kind       = "workflow"
traceloop.entity.name     = "research_agent"
traceloop.entity.path     = "research_agent"
traceloop.entity.input    = "{\"query\":\"summarize Q3 filings\"}"
traceloop.entity.output   = "{\"answer\":\"...\"}"
traceloop.association.properties.session_id = "sess-9c1"
traceloop.association.properties.user_id    = "u-4471"
(no gen_ai.* ; no usage)
```

### Span B — nested agent step  (parent=aa, span_id=bb..)
```
name                      = "planner.agent"
traceloop.span.kind       = "agent"
traceloop.entity.name     = "planner"
traceloop.entity.path     = "research_agent.planner"
traceloop.entity.input    = "{\"goal\":\"decide next tool\"}"
traceloop.entity.output   = "{\"decision\":\"call search then llm\"}"
```

### Span C — LLM generation WITH tool calls  (parent=bb, span_id=cc..)
```
name                             = "openai.chat"
traceloop.span.kind              = "llm"          # NB: "llm", not one of workflow/task/agent/tool
gen_ai.system                    = "openai"
gen_ai.request.model             = "gpt-4o-2024-08-06"
gen_ai.response.model            = "gpt-4o-2024-08-06"
gen_ai.request.temperature       = 0.2
gen_ai.request.max_tokens        = 1024
gen_ai.request.top_p             = 0.95
gen_ai.prompt.0.role             = "system"
gen_ai.prompt.0.content          = "You are a research assistant."
gen_ai.prompt.1.role             = "user"
gen_ai.prompt.1.content          = "Summarize the Q3 filings."
gen_ai.completion.0.role         = "assistant"
gen_ai.completion.0.content      = ""                      # empty; tool call instead of text
gen_ai.completion.0.tool_calls.0.id        = "call_ab12"
gen_ai.completion.0.tool_calls.0.name      = "vector_search"
gen_ai.completion.0.tool_calls.0.arguments = "{\"q\":\"Q3 filings\",\"k\":5}"
gen_ai.usage.prompt_tokens       = 812
gen_ai.usage.completion_tokens   = 143
llm.usage.total_tokens           = 955            # legacy llm.* namespace, NOT gen_ai.usage.total_tokens
gen_ai.usage.cache_read_input_tokens = 640
gen_ai.usage.cost                = 0.004212        # cost on the wire (Traceloop pricing extension)
```

### Span D — tool execution  (parent=cc, span_id=dd..)
```
name                      = "vector_search.tool"
traceloop.span.kind       = "tool"
traceloop.entity.name     = "vector_search"
traceloop.entity.path     = "research_agent.planner.vector_search"
traceloop.entity.input    = "{\"q\":\"Q3 filings\",\"k\":5}"
traceloop.entity.output   = "{\"hits\":5}"
```

### Span E — retrieval (vector DB)  (parent=dd, span_id=ee..)
```
name                      = "pinecone.query"
traceloop.span.kind       = "task"          # Traceloop has NO retrieval kind; vector query is a "task"
traceloop.entity.name     = "pinecone.query"
db.system                 = "pinecone"
db.operation              = "query"
db.vector.query.top_k     = 5
traceloop.entity.input    = "{\"vector\":\"@@@embedding@@@\",\"top_k\":5}"
traceloop.entity.output   = "{\"matches\":[{\"id\":\"doc1\",\"score\":0.91}, ...]}"
```

**Assumptions / divergences from official OTel GenAI (stated):**
- `traceloop.span.kind` real values are `workflow | task | agent | tool`. The **LLM span carries `traceloop.span.kind = "llm"`** in practice (Traceloop adds it for model calls), so I treat `llm` as a fifth de-facto value even though the documented enum is the four above. Flagged.
- Official OTel GenAI uses `gen_ai.usage.input_tokens` / `output_tokens`. Traceloop historically emits **`gen_ai.usage.prompt_tokens` / `completion_tokens`** (OpenAI-shaped legacy names) and puts total under **`llm.usage.total_tokens`**. This is a namespace split not present in clean OTel.
- Official OTel GenAI models messages as log events (`gen_ai.system.message`, etc.) or structured bodies. Traceloop instead **flattens** to `gen_ai.prompt.N.*` / `gen_ai.completion.N.*` span attributes. Flagged.
- `gen_ai.usage.cost`, `llm.usage.total_cost`, and `db.vector.query.top_k` are **not** OTel semconv keys; they are Traceloop/vendor extensions. Assumed present as shown.
- `deployment.environment` is the pre-1.0 OTel key (superseded by `deployment.environment.name`); Traceloop emits the old form. Value carries source casing (`"Production"`).

---

## 2. Field-by-field mapping

### 2.1 Every SOURCE attribute → canonical

| Source attribute / signal | Canonical field | …or `attributes` key | …or raw-preserved | Notes |
|---|---|---|---|---|
| OTLP `span_id` | `id` | — | — | Hex span id (`02-span.md` §4). |
| OTLP `trace_id` | `trace_id` | — | — | Hex. |
| OTLP `parent_span_id` | `parent_span_id` | — | — | Null on Span A (root). |
| OTLP span `name` | `name` | — | — | e.g. `"openai.chat"`. |
| OTLP span start (unixnano) | `start_time` | — | — | RFC3339 UTC; frozen anchor. |
| OTLP span end (unixnano) | `end_time` | — | — | All five are `timed_span`+. |
| OTLP `status.code` (UNSET/OK/ERROR) | `status.code` | — | — | Map ERROR→`error`, else `ok`/`unset`. |
| OTLP `status.message` | `status.message` | — | — | Free text. |
| OTLP exception event / `exception.message` | `status.message` (if error) | `attributes["exception.*"]` | — | Event attrs also preserved. |
| `deployment.environment` = `"Production"` | `environment` = `"production"` | `attributes["deployment.environment"]` (raw resource attr) **and** `llmobs.raw.environment="Production"` | `llmobs.raw.environment` | Sanitize (lowercase) → differs from offered ⇒ set `llmobs.dq.dimension_coerced.environment=true`, increment counter (`08` §2). Frozen. |
| `service.name` | — | `attributes["service.name"]` | — | Resource attr; preserved (`02` §6). Not `name`. |
| `telemetry.sdk.name`, `traceloop.sdk.version` | — | `attributes[...]` (verbatim) | — | Scope/resource attrs preserved. |
| `traceloop.association.properties.session_id` | `session_id` | — | — | Traceloop's session channel. |
| `traceloop.association.properties.user_id` | `user_id` | — | — | User dimension. |
| other `traceloop.association.properties.*` | — | `attributes[...]` | — | Arbitrary user tags; preserved. |
| `traceloop.span.kind = "workflow"` (Span A) | `kind = agent_step` **or** `span`; `raw_kind="workflow"` | — | `raw_kind` carries `"workflow"` | No canonical `workflow` kind. If it orchestrates sub-steps→`agent_step`, else `span`. **Design bug #1.** If falls to `span`: set `llmobs.dq.unmapped_kind=true` (`08` §3). |
| `traceloop.span.kind = "agent"` (Span B) | `kind = agent_step` | — | `raw_kind="agent"` | Clean mapping. |
| `traceloop.span.kind = "llm"` (Span C) | `kind = generation` | — | `raw_kind="llm"` | De-facto value; see assumption. Generation-shaped. |
| `traceloop.span.kind = "tool"` (Span D) | `kind = tool_call` | — | `raw_kind="tool"` | Clean mapping. |
| `traceloop.span.kind = "task"` (Span E) | `kind = retrieval` (only via `db.system` heuristic) **else** `span` | — | `raw_kind="task"` | **Design bug #2**: `task` is generic; retrieval identity is inferred from `db.system`/`db.vector.*`, not declared. If heuristic fails→`span` + `unmapped_kind`. |
| `traceloop.entity.name` | (contributes to) `name` if span `name` weak | `attributes["traceloop.entity.name"]` | — | Preserve verbatim regardless. |
| `traceloop.entity.path` | — | `attributes["traceloop.entity.path"]` | — | Dotted hierarchy path; no canonical field. Preserved. |
| `traceloop.entity.input` | `input` (opaque) | — | — | Passed through opaque (`02` §4.3). |
| `traceloop.entity.output` | `output` (opaque) | — | — | Opaque. |
| `gen_ai.system = "openai"` | — | `attributes["gen_ai.system"]` | — | Provider; not a promoted field. Feeds kernel model resolution but stored in `attributes`. |
| `gen_ai.request.model` | `model` | — | — | The provided model (`02` §5). |
| `gen_ai.response.model` | — | `attributes["gen_ai.response.model"]` | — | `model` takes the request value; response model preserved. If they differ, both retained. |
| `gen_ai.request.temperature` | `model_parameters.temperature` | — | — | Opaque param map. |
| `gen_ai.request.max_tokens` | `model_parameters.max_tokens` | — | — | " |
| `gen_ai.request.top_p` | `model_parameters.top_p` | — | — | " |
| `gen_ai.prompt.0.role` / `.0.content` | (assembled into) `input` | `attributes["gen_ai.prompt.0.*"]` (verbatim) | — | Reassemble flat indices into a messages array for `input`; also preserve raw flat keys (invariant 6). **Design bug #3.** |
| `gen_ai.prompt.1.role` / `.1.content` | (assembled into) `input` | `attributes["gen_ai.prompt.1.*"]` | — | " |
| `gen_ai.completion.0.role` / `.0.content` | (assembled into) `output` | `attributes["gen_ai.completion.0.*"]` | — | Content empty (tool-call turn). |
| `gen_ai.completion.0.tool_calls.0.id/name/arguments` | (assembled into) `output` | `attributes["gen_ai.completion.0.tool_calls.0.*"]` | — | Doubly-flattened list-in-list. **Design bug #3.** |
| `gen_ai.usage.prompt_tokens = 812` | `provided_usage_details["input"] = 812` | — | — | Wire→provided map only (`06` §2). Rename `prompt_tokens`→`input`. |
| `gen_ai.usage.completion_tokens = 143` | `provided_usage_details["output"] = 143` | — | — | Rename→`output`. |
| `llm.usage.total_tokens = 955` | `provided_usage_details["total"] = 955` | — | — | **Design bug #4**: total lives under legacy `llm.usage.*` while input/output are under `gen_ai.usage.*`. Normalizer must read both namespaces. |
| `gen_ai.usage.cache_read_input_tokens = 640` | `provided_usage_details["cache_read"] = 640` | — | — | Well-known `cache_read` (`06` §3.1). NB: do **not** silently subtract from `input`; both stored, kernel decides. |
| `gen_ai.usage.cost = 0.004212` | `provided_cost_details["total"] = 0.004212` | — | — | **Design bug #5**: cost on the wire ⇒ MUST land in `provided_cost_details` (wire-writable), never `cost_details`/`total_cost`/`cost_source` (kernel-only, `06` §2). Kernel later copies with `cost_source="provided"`. |
| `llm.usage.total_cost` (if emitted instead) | `provided_cost_details["total"]` | — | — | Same target; another legacy-namespace duplicate of cost. |
| `db.system = "pinecone"` (Span E) | (retrieval marker for kind heuristic) | `attributes["db.system"]` | — | Drives `task`→`retrieval` inference; preserved. |
| `db.operation = "query"` | — | `attributes["db.operation"]` | — | Preserved. |
| `db.vector.query.top_k = 5` | — | `attributes["db.vector.query.top_k"]` | — | No canonical retrieval-params field; preserved verbatim. |
| Any unlisted `traceloop.*` / `gen_ai.*` / `llm.*` / other attr | — | `attributes[<key>]` verbatim | — | Invariant 6: nothing dropped. |

### 2.2 Every CANONICAL field ← source (coverage check)

| Canonical field | Sourced from | If absent |
|---|---|---|
| `id` | OTLP `span_id` | — always present |
| `project_id` | ingestion context (API key / tenant), not the wire | Required; from auth scope |
| `trace_id` | OTLP `trace_id` | — |
| `parent_span_id` | OTLP `parent_span_id` | null on root (Span A) |
| `kind` | `traceloop.span.kind` (+ `db.*` heuristic) | fallback `span` + `unmapped_kind` |
| `raw_kind` | the literal `traceloop.span.kind` string (`workflow`/`agent`/`llm`/`tool`/`task`) | null if no kind attr |
| `name` | OTLP span `name` (or `traceloop.entity.name`) | — |
| `start_time` | OTLP start | — (frozen anchor) |
| `end_time` | OTLP end | absent ⇒ open span |
| `status` | OTLP `status.code`/`message` | defaults `{code:"unset"}` |
| `environment` | `deployment.environment` (sanitized) | `"default"` |
| `release` | `service.version` / `traceloop.*` build attr if present | null (none in sample) — **gap** |
| `version` | no Traceloop equivalent on the wire | null — **gap** (Traceloop has no per-call prompt/logic version) |
| `session_id` | `traceloop.association.properties.session_id` | null |
| `user_id` | `traceloop.association.properties.user_id` | null |
| `input` | `traceloop.entity.input` (non-LLM) / assembled `gen_ai.prompt.*` (LLM) | null |
| `output` | `traceloop.entity.output` / assembled `gen_ai.completion.*` | null |
| `attributes` | all unmapped `gen_ai.*`, `traceloop.*`, `llm.*`, `db.*`, resource/scope attrs + `llmobs.*` markers | `{}` |
| `model` | `gen_ai.request.model` | null (only on Span C) |
| `model_parameters` | `gen_ai.request.temperature/max_tokens/top_p` | null / `{}` |
| `completion_start_time` | Traceloop has no TTFT attribute | null — **gap** (streaming first-token time not emitted) |
| `provided_usage_details` | `gen_ai.usage.prompt_tokens`→input, `completion_tokens`→output, `llm.usage.total_tokens`→total, `cache_read_input_tokens`→cache_read | `{}` |
| `usage_details` | kernel enrichment (`06` §3.2), NOT the wire | `{}` |
| `provided_cost_details` | `gen_ai.usage.cost` / `llm.usage.total_cost` → `total` | `{}` |
| `cost_details` | kernel enrichment (`06` §4), NOT the wire | `{}` |
| `total_cost` | kernel enrichment | null |
| `cost_source` | kernel: `"provided"` (cost present on wire) | null |
| `pricing_snapshot_ref` | kernel; null when `cost_source="provided"` (`06` §4.1) | null |
| `prompt_ref` | Traceloop emits no prompt-registry linkage | null — **gap** |

Trace entity (`03-trace.md`) is materialized from Span A (root): `id`←trace_id, `name`←`"research_agent.workflow"`, `input`/`output`←Span A entity input/output, `session_id`/`user_id`←association props, `environment`←sanitized `deployment.environment`. `tags` union-merged from any `traceloop.association.properties` list. Shallow trace materialized for the `trace_id` per `03` §3.

---

## 3. Design bugs / unclean landings

### Bug 1 — `traceloop.span.kind` values `workflow`/`task` have no canonical kind
Traceloop's kind enum (`workflow | task | agent | tool`, plus de-facto `llm`) is an orchestration taxonomy, not a semantic one. `workflow` and `task` do not correspond to any of the seven canonical kinds (`span, generation, embedding, tool_call, retrieval, agent_step, guardrail`).
- `workflow` (a top-level orchestration container) must be squeezed into `agent_step` (if it orchestrates sub-steps) or the generic `span` — neither is a faithful representation; a workflow is closer to a *trace* than a span, and the model has no "workflow" span kind by design.
- `task` is a catch-all for any non-LLM unit and collides with retrieval, tool post-processing, and plain compute all at once.
**Why it's a problem:** the source type→kind mapping (`02` §2.2) is lossy here in a way `raw_kind` only partially rescues. `raw_kind` preserves the string for audit, but the queryable `kind` becomes ambiguous, and every `workflow`/`task` that falls to `span` trips `llmobs.dq.unmapped_kind`, inflating the data-quality counter on what is actually normal Traceloop output. Do not invent a canonical `workflow`/`task` kind to paper over it — that would expand the frozen enum (near-permanent commitment, `02` §1.1). It stays a normalizer judgement + `raw_kind` + DQ signal.

### Bug 2 — retrieval is expressed as a `task` with no retrieval marker
The vector-DB query (Span E) carries `traceloop.span.kind = "task"`, not any retrieval type. The only evidence it is a retrieval is the presence of `db.system`/`db.vector.query.top_k` — OTel DB-semconv attributes, orthogonal to Traceloop's kind.
**Why it's a problem:** mapping `task`→`retrieval` requires a heuristic (`db.system` present ⇒ retrieval) that is not part of any declared contract. If the instrumentation omits `db.*` (some vector stores, or a plain HTTP retriever), the span silently degrades to `span` with `raw_kind="task"` and `llmobs.dq.unmapped_kind=true` — a retrieval that never appears as `kind=retrieval` in queries. This is a genuine expressiveness gap in the source dialect, not something the normalizer should hard-code around per-vendor. Surface it via `raw_kind` + DQ, don't fabricate a retrieval kind from a guess.

### Bug 3 — `gen_ai.prompt.N.*` / `completion.N.*` flattened message indices
Messages and tool-calls are lists, but OTLP attributes are a flat `map<string, scalar>`. Traceloop encodes them as index-suffixed keys (`gen_ai.prompt.0.role`, `gen_ai.completion.0.tool_calls.0.arguments`), i.e. a list — and a list-of-lists for tool calls — smeared across the key namespace.
**Why it's a problem:**
- Reassembly into the opaque `input`/`output` requires the normalizer to parse integer indices out of key strings and rebuild ordering. Any gap or non-contiguous index (e.g. a dropped `gen_ai.prompt.2.*` from attribute-count limits) yields a silently shortened message list.
- OTLP span attribute-count/length limits can **truncate** long conversations mid-list. If that truncation happens on `input`/`output` reconstruction, it must be stamped `llmobs.dq.truncated=true` (`02` §7.2) — but the truncation happened upstream in the SDK, invisible to us, so the marker cannot be set honestly. Loss is undetectable at the normalizer.
- Both the reassembled `input`/`output` **and** the raw flat `gen_ai.prompt.N.*` keys must be preserved (invariant 6), duplicating the payload in `attributes`.
This diverges from official OTel GenAI, which moved chat content to structured log events / message bodies specifically to avoid flattening. Don't "fix" by discarding the raw flat keys — invariant 6 forbids it.

### Bug 4 — total tokens under `llm.usage.*` while input/output under `gen_ai.usage.*`
`gen_ai.usage.prompt_tokens` and `gen_ai.usage.completion_tokens` live in the `gen_ai.*` namespace, but the total arrives as `llm.usage.total_tokens` in the **legacy** namespace. Traceloop straddles pre- and post-semconv naming.
**Why it's a problem:** the normalizer must read two namespaces to fill one `provided_usage_details` map, and must also handle the `prompt_tokens`/`completion_tokens` (OpenAI-legacy) vs `input_tokens`/`output_tokens` (OTel semconv) split for the same fields depending on Traceloop version. A version that emits `gen_ai.usage.input_tokens` and one that emits `gen_ai.usage.prompt_tokens` produce identical canonical output only if the normalizer knows every alias. Per `06` §3.2 the kernel synthesizes `total` from the buckets when absent — so a mismatched or stale `llm.usage.total_tokens` could disagree with `input+output`; the model keeps the provided `total` as-is in `provided_usage_details` (no silent overwrite) and the discrepancy is only reconciled in the derived `usage_details`. The aliasing is a source-dialect defect; the normalizer absorbs it via an explicit alias table, not by trusting one namespace.

### Bug 5 — cost arrives on the wire (`gen_ai.usage.cost` / `llm.usage.total_cost`)
Traceloop's pricing extension emits a computed cost as a span attribute. Per `06` §2, the wire is **only** allowed to write the `provided_*` maps; `cost_details`, `total_cost`, `cost_source`, and `pricing_snapshot_ref` are kernel-produced.
**Why it's a problem (and the correct landing):** the tempting shortcut is to drop `gen_ai.usage.cost` straight into `total_cost` / `cost_details` because it is already a resolved number. That is prohibited — it would bypass the provenance machinery and make provided cost indistinguishable from kernel-derived cost after the fact (exactly the Langfuse defect `06` §4 calls out). Correct handling: `gen_ai.usage.cost` → `provided_cost_details["total"]` only. The kernel enrichment then copies it to `cost_details`, sets `cost_source="provided"`, leaves `pricing_snapshot_ref=null`, and short-circuits price-table derivation (`06` §4.1). Additional wrinkle: Traceloop gives a single scalar cost with no per-token-type breakdown, so `provided_cost_details` gets only a `total` key — the input/output cost split is unrecoverable, and any later re-pricing cannot target it (no `pricing_snapshot_ref`, because it is provided not derived). That is an accepted limitation of provided cost, not a bug to route around.

### Bug 6 (secondary) — `deployment.environment` casing + deprecated key
The value `"Production"` is capitalized and uses the pre-1.0 `deployment.environment` key (OTel later renamed to `deployment.environment.name`). Sanitization (`08` §2) lowercases to `"production"`, which differs from the offered value, so `llmobs.dq.dimension_coerced.environment=true` fires and `llmobs.raw.environment="Production"` is stored on **every** Traceloop span — a high-volume, benign DQ signal purely from a casing convention. Not silently coerced (correct), but worth noting it makes the coercion counter noisy for this dialect. Also: if a Traceloop version emits both `deployment.environment` and `deployment.environment.name`, the normalizer needs a precedence rule (prefer the newer key) or the two disagree.

### Coverage gaps (canonical fields Traceloop cannot fill)
- `completion_start_time` — Traceloop emits no time-to-first-token attribute; always null even for streaming.
- `version` — no per-call prompt/logic version on the wire.
- `release` — only if `service.version` happens to be set; usually null.
- `prompt_ref` — Traceloop has no prompt-registry linkage, so no `prompt_ref` is ever produced.
These are honest nulls (source has no data), not lossy mappings — recorded here so the validation is exhaustive on the canonical side.
