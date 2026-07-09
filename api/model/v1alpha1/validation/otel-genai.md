# Validation — OpenTelemetry GenAI

> **Post-review:** the design review ruled on every finding below; the resolutions applied to the spec are tabulated in [`README.md`](README.md#resolutions-applied-post-review-2026-07-09). This worksheet is preserved as the pre-resolution analysis and fixture seed.

**Dialect:** Official OpenTelemetry GenAI semantic conventions (`gen_ai.*`) carried on OTLP spans.
**Target:** LLMObs canonical model `v1alpha1` (`api/model/v1alpha1/02-span.md`, `03-trace.md`, `04-score.md`, `06-usage-cost.md`, `08-data-quality.md`).
**Purpose:** Seed normalizer fixtures + surface unclean landings. Assumptions are marked **[A]**.

Conventions note: OTel is mid-migration. `gen_ai.system` was renamed to `gen_ai.provider.name`; message content moved from **span events** (`gen_ai.{system,user,assistant,tool}.message`, `gen_ai.choice`) to **span attributes** (`gen_ai.input.messages`, `gen_ai.output.messages`). Both encodings are live in the wild, so the representative trace below carries **both** on the generation span to exercise the ambiguity. `[A]`: transport is OTLP/gRPC; ids are the raw OTLP binary rendered lowercase-hex.

---

## 1. Representative agent-shaped trace (OTel GenAI dialect)

One trace `4bf92f3577b34da6a3ce929d0e0e4736`, resource shared by all spans.

**Resource attributes (apply to every span in the batch):**

| Attribute | Value |
|---|---|
| `service.name` | `support-svc` |
| `service.version` | `2.3.1` |
| `deployment.environment.name` | `Production` (note capitalization — deliberate, to exercise sanitization) |
| `telemetry.sdk.name` | `opentelemetry` |
| `telemetry.sdk.language` | `python` |
| `telemetry.sdk.version` | `1.31.0` |
| `user.id` | `u-4417` *(general semconv; GenAI defines no user attribute — see Bug 12)* |

**Instrumentation scope:** name `opentelemetry.instrumentation.openai_v2`, version `2.1b0`.

### Span A — root, agent invocation → `invoke_agent`
| Field / attribute | Value |
|---|---|
| `trace_id` | `4bf92f3577b34da6a3ce929d0e0e4736` |
| `span_id` | `00f067aa0ba902b7` |
| `parent_span_id` | *(empty — root)* |
| `name` | `invoke_agent support-agent` |
| SpanKind | `SERVER` |
| `start_time_unix_nano` | `1720000000000000000` |
| `end_time_unix_nano` | `1720000004500000000` |
| status | `{code: OK}` |
| `gen_ai.operation.name` | `invoke_agent` |
| `gen_ai.provider.name` | `openai` |
| `gen_ai.agent.name` | `support-agent` |
| `gen_ai.agent.id` | `agent-123` |
| `gen_ai.agent.description` | `Answers billing questions` |
| `gen_ai.conversation.id` | `conv-789` |
| `server.address` | `api.openai.com` |

### Span B — nested agent step (sub-agent) → `invoke_agent`
| Field / attribute | Value |
|---|---|
| `span_id` | `1a2b3c4d5e6f7081`, `parent_span_id` = `00f067aa0ba902b7` |
| `name` | `invoke_agent billing-subagent` |
| SpanKind | `INTERNAL` |
| times | start `...000500`, end `...004300` |
| status | `{code: OK}` |
| `gen_ai.operation.name` | `invoke_agent` |
| `gen_ai.agent.name` | `billing-subagent` |
| `gen_ai.conversation.id` | `conv-789` |

### Span C — LLM generation WITH parallel tool calls → `chat`
| Field / attribute | Value |
|---|---|
| `span_id` | `2b3c4d5e6f708192`, `parent_span_id` = `1a2b3c4d5e6f7081` |
| `name` | `chat gpt-4o` |
| SpanKind | `CLIENT` |
| times | start `...001000`, end `...002200` |
| status | `{code: OK}` |
| `gen_ai.operation.name` | `chat` |
| `gen_ai.provider.name` | `openai` |
| `gen_ai.request.model` | `gpt-4o` |
| `gen_ai.response.model` | `gpt-4o-2024-08-06` |
| `gen_ai.request.temperature` | `0.7` |
| `gen_ai.request.max_tokens` | `1024` |
| `gen_ai.request.top_p` | `0.95` |
| `gen_ai.request.frequency_penalty` | `0.0` |
| `gen_ai.request.presence_penalty` | `0.0` |
| `gen_ai.request.stop_sequences` | `["\n\nUser:"]` |
| `gen_ai.response.id` | `chatcmpl-abc123` |
| `gen_ai.response.finish_reasons` | `["tool_calls"]` |
| `gen_ai.usage.input_tokens` | `812` |
| `gen_ai.usage.output_tokens` | `96` |
| `server.address` | `api.openai.com` |
| `server.port` | `443` |
| `gen_ai.input.messages` | JSON: `[{role:system,parts:[{type:text,content:"You are..."}]},{role:user,parts:[{type:text,content:"Refund for order 55?"},{type:image,content:"@@@llmobsMedia:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855@@@"}]}]` *(image part already replaced with a media token by the collector — exercises §7)* |
| `gen_ai.output.messages` | JSON: `[{role:assistant,parts:[{type:tool_call,id:"call_1",name:"get_order",arguments:{id:55}},{type:tool_call,id:"call_2",name:"get_refund_policy",arguments:{}}]}]` *(two **parallel** tool calls in one turn)* |

**Span C ALSO carries legacy span events** (same content, older encoding — present to force the collision, see Bug 1):
| Event name | time | body attributes |
|---|---|---|
| `gen_ai.system.message` | `...001000` | `{content:"You are..."}` |
| `gen_ai.user.message` | `...001000` | `{content:"Refund for order 55?"}` |
| `gen_ai.choice` | `...002200` | `{index:0, finish_reason:"tool_calls", message:{tool_calls:[...call_1, call_2...]}}` |

### Span D — tool execution → `execute_tool`
| Field / attribute | Value |
|---|---|
| `span_id` | `3c4d5e6f70819203`, `parent_span_id` = `1a2b3c4d5e6f7081` |
| `name` | `execute_tool get_order` |
| SpanKind | `INTERNAL` |
| times | start `...002300`, end `...002800` |
| status | `{code: OK}` |
| `gen_ai.operation.name` | `execute_tool` |
| `gen_ai.tool.name` | `get_order` |
| `gen_ai.tool.call.id` | `call_1` |
| `gen_ai.tool.description` | `Fetch an order by id` |
| `gen_ai.tool.type` | `function` |

### Span E — retrieval / vector search (NO gen_ai operation — see Bug 3)
| Field / attribute | Value |
|---|---|
| `span_id` | `4d5e6f7081920314`, `parent_span_id` = `1a2b3c4d5e6f7081` |
| `name` | `vector_search refund_policies` |
| SpanKind | `CLIENT` |
| times | start `...002900`, end `...003400` |
| status | `{code: ERROR}`, `status.message` = `index timeout` |
| `error.type` | `TimeoutError` |
| `db.system` | `qdrant` `[A]` (OTel GenAI has **no** retrieval op; DB semconv is the closest real dialect) |
| `db.operation.name` | `search` `[A]` |
| `server.address` | `qdrant.internal` |

---

## 2. Field-by-field mapping table

Columns: **Source attribute/signal** | **Canonical field** (promoted or generation) | **`attributes` map key** | **Raw-preserved (`llmobs.raw.*`) / dq** | **Notes**. Every source attribute in §1 and every canonical field is covered. Blank = not applicable.

### 2a. OTLP structural fields (all spans)

| Source | Canonical field | attributes key | raw/dq | Notes |
|---|---|---|---|---|
| `trace_id` (bytes) | `Span.trace_id`; `Trace.id` | | | lowercase-hex, 32 chars (`01-entities` §3.1). |
| `span_id` (bytes) | `Span.id` | | | lowercase-hex, 16 chars. Frozen. |
| `parent_span_id` (bytes) | `Span.parent_span_id` | | | `null` when empty ⇒ root. |
| `name` | `Span.name` | | | Passthrough. |
| `start_time_unix_nano` | `Span.start_time` | | | ns→RFC3339 UTC ms+. Frozen / merge anchor. |
| `end_time_unix_nano` | `Span.end_time` | | | Absent ⇒ open span / point_event. |
| OTLP `status.code` (UNSET/OK/ERROR) | `Span.status.code` (`unset`/`ok`/`error`) | | | Direct tri-state alignment (`02-span` §4.1). |
| OTLP `status.message` | `Span.status.message` | | | Free text. |
| **SpanKind** (SERVER/CLIENT/INTERNAL) | — | `otel.span.kind` `[A]` | | **Not** the canonical `kind`; must not be confused with it (Bug 11). Lands in attributes. |
| Instrumentation scope `name`/`version` | — | `otel.scope.name`, `otel.scope.version` `[A]` | | Preserved (invariant 6). |

### 2b. Resource attributes

| Source | Canonical field | attributes key | raw/dq | Notes |
|---|---|---|---|---|
| `service.name` | — | `service.name` | | No promoted home. |
| `service.version` | `Span.release` / `Trace.release` `[A]` | `service.version` (also kept) | | Build/deployment identity ⇒ `release`, not `version` (Bug 10). |
| `deployment.environment.name` = `Production` | `environment` = `production` | | `llmobs.raw.environment` = `Production`; `llmobs.dq.dimension_coerced.environment = true`; counter `field=environment` | Sanitized (lowercase) per `08-data-quality` §2; coercion is observable. Resource-level ⇒ fans out to every span/trace (Bug 13). |
| `telemetry.sdk.name/language/version` | — | same keys | | Preserved. |
| `user.id` = `u-4417` | `user_id` `[A]` | `user.id` (also kept) | | GenAI defines **no** user attribute; sourced from general semconv (Bug 12). |

### 2c. Span A (`invoke_agent`, root)

| Source | Canonical field | attributes key | raw/dq | Notes |
|---|---|---|---|---|
| `gen_ai.operation.name` = `invoke_agent` | `kind = agent_step`; `raw_kind = "invoke_agent"` | | | Per `02-span` §2.2 table. |
| `gen_ai.provider.name` = `openai` | — | `gen_ai.provider.name` | | Read by cost enrichment; no promoted home (Bug 8). |
| `gen_ai.agent.name` | — (feeds `name` derivation only) | `gen_ai.agent.name` | | Agent identity not first-class (Bug 14). |
| `gen_ai.agent.id` | — | `gen_ai.agent.id` | | |
| `gen_ai.agent.description` | — | `gen_ai.agent.description` | | |
| `gen_ai.conversation.id` = `conv-789` | `session_id` | | | Clean mapping. |
| `server.address` | — | `server.address` | | |
| root span ⇒ trace materialization | `Trace.id/project_id/start_time/environment` (shallow), `Trace.session_id`, `Trace.user_id`, `Trace.release` | | | `03-trace` §3 derive-from-root. Trace `name/input/output` have no OTel source (Bug 15). |

### 2d. Span B (nested `invoke_agent`)

| Source | Canonical field | attributes key | raw/dq | Notes |
|---|---|---|---|---|
| `gen_ai.operation.name` = `invoke_agent` | `kind = agent_step`; `raw_kind = "invoke_agent"` | | | |
| `gen_ai.agent.name` | — | `gen_ai.agent.name` | | |
| `gen_ai.conversation.id` | `session_id` | | | |

### 2e. Span C (`chat` generation, parallel tool calls) — `generation_shaped`

| Source | Canonical field | attributes key | raw/dq | Notes |
|---|---|---|---|---|
| `gen_ai.operation.name` = `chat` | `kind = generation`; `raw_kind = "chat"` | | | `02-span` §2.2. |
| `gen_ai.provider.name` = `openai` | — | `gen_ai.provider.name` | | Needed for cost derivation but only in attributes (Bug 8). |
| `gen_ai.request.model` = `gpt-4o` | `model` | | | The "provided" model (`02-span` §5). |
| `gen_ai.response.model` = `gpt-4o-2024-08-06` | — | `gen_ai.response.model` | | Better cost key, but **no promoted home**; enrichment must read attributes (Bug 7). |
| `gen_ai.request.temperature` | `model_parameters.temperature` | *(see note)* | | Folded into opaque `model_parameters` (Bug 6). |
| `gen_ai.request.max_tokens` | `model_parameters.max_tokens` | | | |
| `gen_ai.request.top_p` | `model_parameters.top_p` | | | |
| `gen_ai.request.frequency_penalty` | `model_parameters.frequency_penalty` | | | |
| `gen_ai.request.presence_penalty` | `model_parameters.presence_penalty` | | | |
| `gen_ai.request.stop_sequences` | `model_parameters.stop_sequences` | | | Array value. |
| `gen_ai.response.id` | — | `gen_ai.response.id` | | Provider response id; no home. |
| `gen_ai.response.finish_reasons` = `["tool_calls"]` | — | `gen_ai.response.finish_reasons` | | Not auto-mapped to status; `content_filter`/`length` do not raise `error` (Bug 16). |
| `gen_ai.usage.input_tokens` = 812 | `provided_usage_details.input` | | | `06` §3.1 well-known. Kernel then sets `usage_details`. |
| `gen_ai.usage.output_tokens` = 96 | `provided_usage_details.output` | | | Kernel synthesizes `total` = 908. |
| *(no cache/reasoning/audio/image usage in OTel)* | `provided_usage_details.{cache_read,cache_write,reasoning,audio,image}` = *unset* | | | One-directional gap: these well-known keys are unfillable from OTel (Bug 9). |
| *(OTel has no cost attributes)* | `provided_cost_details = {}` ⇒ `cost_source = derived` (or null) | | | Cost always kernel-derived from usage+price; `provided_cost_details` never populated (expected, not a bug). |
| `server.address`, `server.port` | — | `server.address`, `server.port` | | |
| `gen_ai.input.messages` (JSON) | `input` (opaque) | | | Clean when the **attribute** encoding is used. Contains media token — resolves to MediaReference (`02-span` §7). |
| `gen_ai.output.messages` (JSON, 2 tool_calls) | `output` (opaque) | | | Parallel tool calls both live inside one opaque `output`; correlation to Span D/next tool span is only via `tool.call.id` string (Bug 2). |
| span event `gen_ai.system.message` | *(reconstruct into `input`)* | `otel.events[]` `[A]` fallback | | **Collides** with `gen_ai.input.messages` (Bug 1). |
| span event `gen_ai.user.message` | *(reconstruct into `input`)* | | | Event timestamp has no home (Bug 1). |
| span event `gen_ai.choice` | *(reconstruct into `output`)* | | | |

**model_parameters note:** the discrete `gen_ai.request.*` keys are gathered into the single opaque `model_parameters` object; because `model_parameters` is a first-class generation field (`02-span` §5) it is an exception to "everything else goes to `attributes`", so these keys are **not** duplicated into `attributes`. This membership decision is heuristic — see Bug 6.

### 2f. Span D (`execute_tool`)

| Source | Canonical field | attributes key | raw/dq | Notes |
|---|---|---|---|---|
| `gen_ai.operation.name` = `execute_tool` | `kind = tool_call`; `raw_kind = "execute_tool"` | | | `02-span` §2.2. |
| `gen_ai.tool.name` = `get_order` | — (feeds `name`) | `gen_ai.tool.name` | | Tool identity not first-class. |
| `gen_ai.tool.call.id` = `call_1` | — | `gen_ai.tool.call.id` | | Only link back to Span C's `output` tool_call (Bug 2). |
| `gen_ai.tool.description` | — | `gen_ai.tool.description` | | |
| `gen_ai.tool.type` = `function` | — | `gen_ai.tool.type` | | |

### 2g. Span E (retrieval — no gen_ai op)

| Source | Canonical field | attributes key | raw/dq | Notes |
|---|---|---|---|---|
| *(absent `gen_ai.operation.name`)* | `kind = span` (fallback), `raw_kind = null`; `db.operation.name`=`search` cannot set `retrieval` | | `llmobs.dq.unmapped_kind = true`; counter `raw_kind` | GenAI has no retrieval op ⇒ a genuine retrieval degrades to generic `span` (Bug 3). To get `kind=retrieval` the normalizer must special-case `db.*`/vendor keys `[A]`. |
| `db.system` = `qdrant` | — | `db.system` | | |
| `db.operation.name` = `search` | — | `db.operation.name` | | If used to force `raw_kind`/`kind=retrieval`, that is a normalizer heuristic, not GenAI semconv. |
| `server.address` | — | `server.address` | | |
| OTLP `status.code = ERROR` | `status.code = error` | | | |
| OTLP `status.message` = `index timeout` | `status.message` | | | |
| `error.type` = `TimeoutError` | — | `error.type` | | No promoted home; parallel to `status` and can disagree with it (Bug 17). |

### 2h. Canonical fields with NO source in this dialect (completeness check)

| Canonical field | Source in OTel GenAI? | Notes |
|---|---|---|
| `project_id` | none | Supplied by ingestion auth context, not the wire (`01-entities` §1). |
| `version` (per-call prompt/logic version) | **none** | `service.version` maps to `release`; GenAI has no per-call version (Bug 10). |
| `completion_start_time` (TTFT) | **none** | Exists only as a **metric** (`gen_ai.server.time_to_first_token`), not a span attribute ⇒ always null (Bug 18). |
| `prompt_ref` | none | GenAI has no prompt-registry linkage. |
| `pricing_snapshot_ref`, `cost_details`, `total_cost`, `cost_source`, `usage_details` | kernel-produced | Never wire-writable (`06` §2). |
| `Trace.name` / `Trace.input` / `Trace.output` / `Trace.tags` | **none** | No trace entity in OTLP; only derivable from root span, else null/`[]` (Bug 15). |
| `Score` / `ScoreConfig` (whole entity) | none | OTel GenAI emits no evaluation/score signal; scores arrive via a different path. |

---

## Design bugs / unclean landings

**Bug 1 — Span *events* are log-records-on-a-span, not point-event child spans, and collide with the attribute encoding.**
`gen_ai.system.message` / `user.message` / `assistant.message` / `gen_ai.choice` are OTLP *events*: timestamped records nested inside one span, each with its own body. The canonical model has no "event nested in a span" concept — a point-in-time thing is a separate `point_event` **span** (`02-span` §3.1), not a sub-record. So the normalizer must (a) reconstruct the ordered message list from N events into the single opaque `input`/`output`, and (b) discard each event's individual timestamp (no canonical home). Worse, when an instrumentation emits **both** the new `gen_ai.input.messages`/`output.messages` attributes **and** the legacy events (as many transitional SDKs do), the same content arrives twice via two different mechanisms that map to the same `input`/`output` — the normalizer needs a documented precedence rule or it double-counts / conflicts. This is a structural mismatch, not a field rename.

**Bug 2 — Parallel tool calls in one generation have no first-class correlation to their `execute_tool` spans.**
Span C's `gen_ai.output.messages` contains two tool_call parts (`call_1`, `call_2`); each is executed in a separate `execute_tool` span (Span D and a sibling) carrying `gen_ai.tool.call.id`. The only thread connecting a generation's emitted tool call to the span that ran it is the `call.id` **string buried inside opaque `output`** on one side and an attribute on the other. The canonical model exposes no tool-call/tool-result linkage field, so this correlation is unqueryable and is invisible to the promoted surface — a consumer must parse opaque `output` JSON and string-match against child-span attributes. For fan-out (N parallel calls) this is fragile.

**Bug 3 — Retrieval has no `gen_ai.operation.name`, so a real retrieval degrades to generic `span`.**
OTel GenAI defines operations for chat/completion/embeddings/execute_tool/invoke_agent/create_agent but **nothing** for retrieval / vector search. The canonical `kind=retrieval` therefore has no GenAI trigger. Span E is a genuine vector search, but from `gen_ai.*` alone the normalizer must fall back to `kind=span` and stamp `llmobs.dq.unmapped_kind`. Getting `kind=retrieval` requires the normalizer to special-case *other* dialects (DB semconv `db.system`/`db.operation`, or vendor keys) — i.e. the "OTel GenAI" adapter cannot classify retrieval using GenAI at all. This is a coverage hole in the source taxonomy that pushes classification logic outside the dialect.

**Bug 6 — `model_parameters` arrives as many discrete `gen_ai.request.*` keys, not one object, and membership is heuristic.**
Canonical `model_parameters` is a single opaque object; OTel sends `gen_ai.request.temperature`, `.max_tokens`, `.top_p`, `.frequency_penalty`, `.presence_penalty`, `.stop_sequences`, `.top_k`, `.seed`, … as separate flat attributes. The normalizer must decide **which** `gen_ai.request.*` keys are "parameters" (fold into `model_parameters`) versus which are not (`gen_ai.request.model` → `model`). There is no closed list, so a new `gen_ai.request.*` key is silently ambiguous. Compounding it: `model_parameters` is a first-class generation field, an exception to invariant 6's "everything else → `attributes`", so a key folded there is **not** also mirrored into `attributes`. If the normalizer guesses wrong (folds a non-parameter, or leaves a real parameter in `attributes`), the two stores disagree and there is no round-trip guarantee. The spec should pin the request.* → model_parameters membership rule.

**Bug 7 — `gen_ai.response.model` has no promoted home despite being the better cost/identity key.**
Only `gen_ai.request.model` maps to the promoted `model`. The actually-served model (`gen_ai.response.model`, e.g. `gpt-4o-2024-08-06`) — the correct key for price lookup and precise identity — lands in `attributes`. Cost enrichment (`06`) must therefore reach into `attributes["gen_ai.response.model"]` rather than a promoted field, which is exactly the "kernel-resolved model identity is a `06`-derived attribute" hedge — but the derivation input isn't first-class, so it is easy to get wrong or miss.

**Bug 8 — provider (`gen_ai.provider.name`) is enrichment-critical yet only in `attributes`.**
Cost derivation is (provider, model) → price. `gen_ai.provider.name` has no promoted home; like response.model it survives only in `attributes`. The two inputs the cost enrichment step most needs (provider + served model) both live in the open map, making the enrichment contract depend on string keys rather than typed fields.

**Bug 9 — well-known usage keys `cache_read`/`cache_write`/`reasoning`/`audio`/`image` are unfillable from OTel.**
OTel GenAI standardizes only `gen_ai.usage.input_tokens` / `output_tokens`. The canonical `06` well-known set includes cache read/write, reasoning, audio, image — none have a stable GenAI attribute. So cross-provider aggregation on those keys silently omits every OTel-sourced span even when the underlying provider billed them (they may be buried in vendor-specific attributes the normalizer can't map). One-directional coverage gap.

**Bug 10 / 18 — `version` and `completion_start_time` are canonical fields with no OTel source.**
`service.version` is build identity and maps cleanly to `release`; there is **no** OTel attribute for the per-call logic/prompt `version`, so it is always null from this dialect. Time-to-first-token (`completion_start_time`) exists in OTel only as a **metric** (`gen_ai.server.time_to_first_token`), never as a span attribute, so a canonical generation field can never be populated over OTLP spans. Both are canonical fields with a structurally absent source — fine, but fixtures must assert null, and any UI depending on TTFT will always be empty for OTel ingestion.

**Bug 11 — OTLP `SpanKind` vs canonical `kind` name collision.**
OTLP's `SpanKind` (SERVER/CLIENT/INTERNAL/PRODUCER/CONSUMER) is transport-topology, unrelated to the canonical `kind` (which derives from `gen_ai.operation.name`). They share the word "kind." A naive normalizer could map `SpanKind` onto canonical `kind` and get nonsense. The mapping must explicitly ignore `SpanKind` for `kind` (relegate it to `attributes`) and drive `kind` from `gen_ai.operation.name` — a fixture must lock this in to prevent regression.

**Bug 12 — `user_id` has no GenAI source; it is borrowed from general semconv.**
GenAI defines `gen_ai.conversation.id` (→ `session_id`) but no user concept. `user_id` can only be filled from the general `user.id` (or deprecated `enduser.id`) if the instrumentation happens to set it. So a promoted dimension is populated from outside the dialect and is usually absent.

**Bug 13 — `deployment.environment.name` is a resource attribute fanned to every span.**
`environment` is per-span/per-trace in the canonical model, but its OTel source is a **resource** attribute shared by the whole export batch. The normalizer must copy it onto every span and the materialized trace, and run sanitization (`Production` → `production`) once per entity with the dq marker on each. Not lossy, but a fan-out the fixtures must cover (and a place where a batch mixing environments — legal in OTLP if resources differ — must be handled per-resource, not per-batch).

**Bug 14 — agent and tool identity (`gen_ai.agent.*`, `gen_ai.tool.*`) collapse into `attributes`.**
`invoke_agent` and `execute_tool` carry structured identity (`agent.id/name/description`, `tool.name/call.id/type`). None has a promoted home, so all of it lands in the open map. That satisfies invariant 6 (nothing lost), but the primary business identity of an agent step / tool call is not queryable as a first-class field — only via `attributes` key lookups.

**Bug 15 — no trace entity in OTLP: `Trace.name`/`input`/`output`/`tags` have no source.**
OTLP is a flat span stream; the canonical `Trace` must be materialized from the root span (`03-trace` §3). But OTel carries no trace-level name/input/output/tags, so the trace is perpetually "shallow" for those fields (null / `[]`) unless the normalizer synthesizes them from the root span's own `name`/`input`/`output` — a promotion the spec allows but does not require identical values for, so trace-level views over OTel data are structurally thin.

**Bug 16 — `finish_reasons` is not wired to `status`.**
`gen_ai.response.finish_reasons = ["content_filter"]` or `["length"]` signals a degraded/failed generation, but OTLP span `status` may still be `OK`. The mapping leaves `finish_reasons` in `attributes` and does not raise `status.code=error`, so a content-filtered or truncated generation reads as a success. Whether that should map to `error` is a policy the spec doesn't state.

**Bug 17 — `error.type` and span `status` are independent and can disagree.**
Some GenAI instrumentations set `error.type` without setting OTLP `status.code=ERROR` (and vice versa). The normalizer maps `status.code`→`status.code` and dumps `error.type` into `attributes`. If a span has `error.type` but `status=UNSET/OK`, the canonical `status` stays non-error while the error detail hides in `attributes` — an inconsistency with no reconciliation rule. The spec should state whether a present `error.type` forces `status.code=error`.

---

**Fixture-relevant invariants confirmed clean:** id hex rendering, `status` tri-state, `gen_ai.conversation.id`→`session_id`, `gen_ai.usage.*`→`provided_usage_details.{input,output}`, the operation→kind table (chat/embeddings/execute_tool/invoke_agent/create_agent), `raw_kind` preservation, the media token passthrough in `input`, environment sanitization + dq signal, and provided-cost-empty ⇒ kernel-derived cost. The unclean landings above (Bugs 1–3, 6–18) are the ones that should drive new fixtures and, where flagged, spec clarifications.
