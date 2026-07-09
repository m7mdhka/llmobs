# Validation — OpenInference (Arize) OTLP Semantic Conventions

**Dialect:** OpenInference span-level semantic conventions carried over OTLP.
**Target:** LLMObs canonical data model `v1alpha1` (`/Users/m7mdhka/Desktop/llmobs/api/model/v1alpha1/`).
**Convention:** OpenInference emits all its semantics as **flat OTLP span attributes** (dotted keys, indexed arrays flattened as `prefix.<N>.suffix`). The OTLP envelope (`span_id`, `trace_id`, `parent_span_id`, `name`, `start/end` unix-nano, `status`) is standard OTel; the `openinference.*`/`llm.*`/`tool.*`/`retrieval.*`/`embedding.*` keys are the dialect. Where an exact key is uncertain it is flagged **[ASSUMPTION]**.

---

## 1. Representative agent-shaped trace (OpenInference)

One trace, `trace_id = 7b1f...e9`, six spans. Rendered as OTLP spans with their attribute bags.

### Span A — root AGENT (`span_id=a1`, parent=none)

```
name                       = "customer_support_agent"
openinference.span.kind    = "AGENT"
input.value                = "Refund my last order and confirm the policy."
input.mime_type            = "text/plain"
output.value               = "Refunded order #5512. Policy: 30-day returns."
output.mime_type           = "text/plain"
session.id                 = "sess-88f2"
user.id                    = "user-4471"
metadata                   = "{\"app\":\"support\",\"env\":\"Production\",\"release\":\"git-9ac1\",\"version\":\"agent-v7\"}"
tag.tags.0                 = "priority:high"
tag.tags.1                 = "channel:web"
# OTLP resource attrs (on ResourceSpans, shared):
service.name               = "support-svc"
deployment.environment     = "Production"
# OTLP status:
status.code                = OK
```

### Span B — CHAIN orchestration (`span_id=b2`, parent=a1)

```
name                       = "plan_and_execute"
openinference.span.kind    = "CHAIN"
input.value                = "{\"goal\":\"refund+policy\"}"
input.mime_type            = "application/json"
output.value               = "{\"steps\":3}"
output.mime_type           = "application/json"
```

### Span C — LLM generation WITH tool calls (`span_id=c3`, parent=b2)

```
name                                              = "chat_completions"
openinference.span.kind                           = "LLM"
llm.provider                                      = "openai"          # [ASSUMPTION on exact value set]
llm.system                                        = "openai"         # [ASSUMPTION]
llm.model_name                                    = "gpt-4o-2024-08-06"
llm.invocation_parameters                         = "{\"temperature\":0.2,\"max_tokens\":512,\"top_p\":1}"
llm.input_messages.0.message.role                 = "system"
llm.input_messages.0.message.content              = "You are a support agent."
llm.input_messages.1.message.role                 = "user"
llm.input_messages.1.message.content              = "Refund my last order..."
llm.output_messages.0.message.role                = "assistant"
llm.output_messages.0.message.content             = ""
llm.output_messages.0.message.tool_calls.0.tool_call.id               = "call_01"   # [ASSUMPTION: .id present]
llm.output_messages.0.message.tool_calls.0.tool_call.function.name    = "issue_refund"
llm.output_messages.0.message.tool_calls.0.tool_call.function.arguments= "{\"order_id\":\"5512\"}"
llm.token_count.prompt                            = 812
llm.token_count.completion                        = 43
llm.token_count.total                             = 855
llm.token_count.prompt_details.cache_read         = 128
llm.token_count.prompt_details.cache_write        = 0                 # [ASSUMPTION: symmetric key]
llm.token_count.completion_details.reasoning      = 16                # [ASSUMPTION]
input.value                                       = "{...full request JSON...}"
input.mime_type                                   = "application/json"
output.value                                      = "{...full response JSON...}"
output.mime_type                                  = "application/json"
metadata                                          = "{\"cost\":{\"total\":0.0041}}"  # provider-specific; see bug B7
```

### Span D — TOOL execution (`span_id=d4`, parent=b2)

```
name                       = "issue_refund"
openinference.span.kind    = "TOOL"
tool.name                  = "issue_refund"
tool.description           = "Issues a refund for an order id."
tool.parameters            = "{\"type\":\"object\",\"properties\":{\"order_id\":{\"type\":\"string\"}}}"
tool_call.function.name    = "issue_refund"
tool_call.function.arguments = "{\"order_id\":\"5512\"}"
input.value                = "{\"order_id\":\"5512\"}"
input.mime_type            = "application/json"
output.value               = "{\"status\":\"refunded\",\"amount\":42.00}"
output.mime_type           = "application/json"
```

### Span E — RETRIEVER (`span_id=e5`, parent=b2)

```
name                       = "policy_search"
openinference.span.kind    = "RETRIEVER"
input.value                = "return policy"
retrieval.documents.0.document.id       = "doc-9"
retrieval.documents.0.document.content  = "Returns accepted within 30 days."
retrieval.documents.0.document.score    = 0.91
retrieval.documents.0.document.metadata = "{\"source\":\"policy.pdf\",\"page\":2}"
retrieval.documents.1.document.id       = "doc-3"
retrieval.documents.1.document.content  = "Refunds processed in 5 business days."
retrieval.documents.1.document.score    = 0.77
retrieval.documents.1.document.metadata = "{\"source\":\"policy.pdf\",\"page\":5}"
```

### Span F — EMBEDDING (`span_id=f6`, parent=e5)

```
name                          = "embed_query"
openinference.span.kind       = "EMBEDDING"
embedding.model_name          = "text-embedding-3-small"
embedding.embeddings.0.embedding.text    = "return policy"
embedding.embeddings.0.embedding.vector  = [0.01, -0.22, ...]   # float array
llm.token_count.prompt        = 3
llm.token_count.total         = 3
```

---

## 2. Field-by-field mapping table

Legend for target column: **P** = promoted/typed field; **A** = lands in `attributes` map under the given key; **R** = raw-preserved under reserved `llmobs.*`; **G** = generation field (generation_shaped only).

### 2.1 OTLP envelope (all spans)

| Source attribute/signal | Canonical field | attributes-map key | raw-preserved | Notes |
|---|---|---|---|---|
| OTLP `span_id` (hex) | `id` (P) | — | — | Direct. `02` §4, `01` §3. |
| OTLP `trace_id` (hex) | `trace_id` (P) | — | — | Direct. |
| OTLP `parent_span_id` | `parent_span_id` (P) | — | — | `null` on span A (root). |
| OTLP span `name` | `name` (P) | — | — | Direct. |
| OTLP `start_time_unix_nano` | `start_time` (P) | — | — | ns→RFC3339 ms; identity anchor. |
| OTLP `end_time_unix_nano` | `end_time` (P) | — | — | Direct. |
| OTLP `status.code` (OK/ERROR/UNSET) | `status.code` (P) | — | — | `02` §4.1 tri-state. |
| OTLP `status.message` | `status.message` (P) | — | — | Free text. |
| — (no source) | `project_id` (P) | — | — | Injected from ingest auth/tenant, not from the wire. |
| OTLP resource `service.name` | — | `service.name` (A) | — | Resource attr preserved verbatim (`02` §6, invariant 6). |
| OTLP resource `deployment.environment` = "Production" | `environment` (P) = `"production"` | `deployment.environment` (A, verbatim) | `llmobs.raw.environment` = "Production" (R) | Sanitized (lowercase) `08` §2 → coercion differs from offered → DQ marker `llmobs.dq.dimension_coerced.environment=true` + counter. |

### 2.2 Span A — AGENT

| Source attribute | Canonical field | attributes key | raw-preserved | Notes |
|---|---|---|---|---|
| `openinference.span.kind` = "AGENT" | `kind` = `agent_step` (P) | — | `raw_kind` = "AGENT" (P) | `02` §2.2 table. |
| `input.value` | `input` (P, opaque) | — | — | `input.mime_type` is metadata about it (see bug B4). |
| `input.mime_type` = "text/plain" | — | `openinference.input.mime_type` (A) **[ASSUMPTION on key retention]** | — | No canonical home; must be preserved, not dropped. Bug B4. |
| `output.value` | `output` (P, opaque) | — | — | |
| `output.mime_type` | — | `openinference.output.mime_type` (A) | — | Bug B4. |
| `session.id` | `session_id` (P) | — | — | Direct. |
| `user.id` | `user_id` (P) | — | — | Direct. |
| `metadata` (JSON string) | — | see notes | — | **Unpacked**: `metadata.env`→`environment` candidate, `metadata.release`→`release` (P), `metadata.version`→`version` (P). Remaining keys (`app`) → `attributes` under `metadata.app` (or nested). Bug B8. |
| `tag.tags.0`, `tag.tags.1` | trace `tags` (P, on the derived trace) | — | — | Span has no `tags` field (trace-only). Set on trace via root-span promotion (`03` §3). Span itself retains them under `attributes` as flattened `tag.tags.0/1` unless reconstructed. Bug B5. |
| — | `attributes` = `{}` default | — | — | `02` §4. |

### 2.3 Span B — CHAIN

| Source attribute | Canonical field | attributes key | raw-preserved | Notes |
|---|---|---|---|---|
| `openinference.span.kind` = "CHAIN" | `kind` = `agent_step` **or** `span` (P) | — | `raw_kind` = "CHAIN" (P) | **Ambiguous mapping** — `02` §2.2: `agent_step` if it orchestrates sub-steps, else `span`. This CHAIN has children ⇒ `agent_step`. Bug B1. |
| `input.value` / `.mime_type` | `input` (P) / attr | — | — | As §2.2. |
| `output.value` / `.mime_type` | `output` (P) / attr | — | — | |

### 2.4 Span C — LLM generation with tool calls

| Source attribute | Canonical field | attributes key | raw-preserved | Notes |
|---|---|---|---|---|
| `openinference.span.kind` = "LLM" | `kind` = `generation` (P) | — | `raw_kind` = "LLM" (P) | `02` §2.2. Span is `generation_shaped`. |
| `llm.model_name` | `model` (G) | — | — | Direct. `02` §5. |
| `llm.provider` / `llm.system` | — | `llm.provider`, `llm.system` (A) | — | No canonical field; preserved. Kernel-resolved model identity is a `06`-derived attribute, not promoted. |
| `llm.invocation_parameters` (JSON string) | `model_parameters` (G, object) | — | — | **Type mismatch**: source is a serialized JSON *string*; target is an *object*. Normalizer must parse; if parse fails it must fall back to preserving the raw string somewhere. Bug B2. |
| `llm.input_messages.N.message.role/content` | `input` (G/P, opaque) — reconstructed | flattened keys `llm.input_messages.*` (A) if not reconstructed | — | **Flattened indexed array** — either reconstruct a messages array into `input`, or land as many discrete attributes. Bug B3. |
| `llm.output_messages.0.message.role` = "assistant" | `output` (P) — reconstructed | `llm.output_messages.0.message.role` (A) | — | Bug B3. |
| `llm.output_messages.0.message.content` | `output` (P) | `...content` (A) | — | Bug B3. |
| `llm.output_messages.0.message.tool_calls.0.tool_call.id` | `output` (P) — reconstructed | `...tool_calls.0.tool_call.id` (A) | — | **Nested indexed array inside an indexed array.** Bug B6. |
| `...tool_calls.0.tool_call.function.name` = "issue_refund" | `output` (P) — reconstructed | `...function.name` (A) | — | Bug B6. No promoted "tool_calls" field. |
| `...tool_calls.0.tool_call.function.arguments` (JSON string) | `output` (P) — reconstructed | `...function.arguments` (A) | — | Bug B6. |
| `llm.token_count.prompt` = 812 | `provided_usage_details.input` = 812 (G) | — | — | `06` §3.1 well-known `input`. Wire writes **provided_*** only (`06` §2); `usage_details` computed by kernel. |
| `llm.token_count.completion` = 43 | `provided_usage_details.output` = 43 (G) | — | — | well-known `output`. |
| `llm.token_count.total` = 855 | `provided_usage_details.total` = 855 (G) | — | — | well-known `total`. |
| `llm.token_count.prompt_details.cache_read` = 128 | `provided_usage_details.cache_read` = 128 (G) | — | — | well-known `cache_read`. Note double-count risk vs `input` (see bug B9). |
| `llm.token_count.prompt_details.cache_write` = 0 **[ASSUMPTION]** | `provided_usage_details.cache_write` = 0 (G) | — | — | well-known `cache_write` (assumed key). |
| `llm.token_count.completion_details.reasoning` = 16 **[ASSUMPTION]** | `provided_usage_details.reasoning` = 16 (G) | — | — | well-known `reasoning`. |
| `metadata.cost.total` (provider-specific, in `metadata` JSON) | `provided_cost_details.total` (G)? | `metadata.cost` (A) | — | Ambiguous — OpenInference has **no standard cost attribute**; cost only shows up ad-hoc in `metadata`. Bug B7. |
| `input.value`/`output.value`/mime | `input`/`output` (P) / attr | — | — | As §2.2. |
| (kernel-produced) | `usage_details`, `cost_details`, `total_cost`, `cost_source`, `pricing_snapshot_ref`, `completion_start_time` | — | — | Not wire-writable (`06` §2). `completion_start_time` has **no OpenInference source** → null unless a TTFT attr exists. Bug B10. |

### 2.5 Span D — TOOL

| Source attribute | Canonical field | attributes key | raw-preserved | Notes |
|---|---|---|---|---|
| `openinference.span.kind` = "TOOL" | `kind` = `tool_call` (P) | — | `raw_kind` = "TOOL" (P) | `02` §2.2. |
| `tool.name` | `name` (P)? or attr | `tool.name` (A) | — | `name` already carries OTLP span name ("issue_refund" coincides here). No promoted "tool name" field; must land in `attributes`. |
| `tool.description` | — | `tool.description` (A) | — | Preserved. |
| `tool.parameters` (JSON schema string) | — | `tool.parameters` (A) | — | Preserved verbatim. |
| `tool_call.function.name` | `input` (P) — part of call, or attr | `tool_call.function.name` (A) | — | Duplicates `tool.name`; no promoted home. |
| `tool_call.function.arguments` (JSON string) | `input` (P) — or attr | `tool_call.function.arguments` (A) | — | Overlaps `input.value`. Two representations of the same thing. Bug B11. |
| `input.value`/`output.value`/mime | `input`/`output` (P) / attr | — | — | |

### 2.6 Span E — RETRIEVER

| Source attribute | Canonical field | attributes key | raw-preserved | Notes |
|---|---|---|---|---|
| `openinference.span.kind` = "RETRIEVER" | `kind` = `retrieval` (P) | — | `raw_kind` = "RETRIEVER" (P) | `02` §2.2. |
| `input.value` = "return policy" | `input` (P) | — | — | |
| `retrieval.documents.0.document.id` | `output` (P) — reconstructed | `retrieval.documents.0.document.id` (A) | — | **Structured flattened array** — the documents list is the retrieval's real output but there is no promoted "documents" field. Bug B3/B12. |
| `retrieval.documents.0.document.content` | `output` (P) — reconstructed | `...content` (A) | — | Bug B12. |
| `retrieval.documents.0.document.score` = 0.91 | — | `...score` (A) | — | Per-doc relevance score. Not an LLMObs `Score` entity (those attach to spans/traces, not sub-rows). Lives only as attribute. Bug B12. |
| `retrieval.documents.0.document.metadata` (JSON) | — | `...metadata` (A) | — | Nested JSON-in-flattened-array. Bug B12. |
| `retrieval.documents.1.*` (same 4 keys) | as above | `...1.*` (A) | — | Second document → 4 more discrete attribute keys. Cardinality grows with result count. Bug B12. |

### 2.7 Span F — EMBEDDING

| Source attribute | Canonical field | attributes key | raw-preserved | Notes |
|---|---|---|---|---|
| `openinference.span.kind` = "EMBEDDING" | `kind` = `embedding` (P) | — | `raw_kind` = "EMBEDDING" (P) | `02` §2.2. `generation_shaped`. |
| `embedding.model_name` | `model` (G) | — | — | Distinct key from `llm.model_name`; normalizer must check both. |
| `embedding.embeddings.0.embedding.text` | `input` (P) — reconstructed | `embedding.embeddings.0.embedding.text` (A) | — | Flattened array. Bug B3. |
| `embedding.embeddings.0.embedding.vector` (float[]) | — | `...vector` (A) | — | Large float array as an attribute value; potentially huge. Bug B13. |
| `llm.token_count.prompt` = 3 | `provided_usage_details.input` = 3 (G) | — | — | Embedding usage; `input` well-known. |
| `llm.token_count.total` = 3 | `provided_usage_details.total` = 3 (G) | — | — | |

### 2.8 Canonical-field coverage check (fields with NO OpenInference source)

| Canonical field | Source in OpenInference? | Disposition |
|---|---|---|
| `project_id` | No | Injected from ingest tenant context. |
| `release` | Only inside `metadata` JSON if the app put it there | Non-standard; unpack from `metadata` heuristically or null. Bug B8. |
| `version` | Only inside `metadata` JSON | Same. Bug B8. |
| `completion_start_time` | No standard attr | Null. Bug B10. |
| `usage_details`, `cost_details`, `total_cost`, `cost_source`, `pricing_snapshot_ref` | Kernel-derived | Not from wire (`06` §2). |
| `prompt_ref` | No OpenInference prompt-linkage attr | Null (OpenInference has no prompt-management linkage). |
| trace `name`/`input`/`output`/`tags`/`status` | From root span A promotion | `03` §3 root-span→trace promotion. |

---

## 3. Design bugs / unclean landings

### B1 — CHAIN kind is context-dependent, not attribute-determined
`02` §2.2 maps OpenInference `CHAIN` to `agent_step` **"if it orchestrates sub-steps, else `span`."** That predicate cannot be evaluated from the span's own attributes — it depends on whether *other* spans name this one as `parent_span_id`, which may not be known when the span is normalized (streaming/out-of-order arrival). Two normalizers, or the same normalizer at two arrival times, can assign different `kind` to the same `CHAIN` span. Because `kind` is **frozen after first write** (`02` §2, `05` §3), whichever value lands first wins and a later contradicting decision raises a `frozen_field_conflict` for a purely structural reason. The mapping rule is under-determined for a frozen field.

### B2 — `llm.invocation_parameters` is a JSON string; `model_parameters` is an object
Source (`llm.invocation_parameters`) is a serialized JSON **string**; the canonical `model_parameters` (`02` §5) is typed **object**. The normalizer must JSON-parse a wire string into a structured field. This is a real transformation with failure modes: malformed JSON, or a provider that put a non-object there. `input`/`output` are explicitly opaque and *not* parsed (`02` §4.3), but `model_parameters` is the one generation field that demands parsing — asymmetric and error-prone. On parse failure there is no specified fallback slot, risking a dropped value (violating invariant 6) unless the raw string is preserved in `attributes`.

### B3 — Flattened indexed message arrays land as N attributes, not a reconstructed `input`/`output`
OpenInference emits `llm.input_messages.<i>.message.role/content` (and the output equivalents) as **flattened OTLP keys**. The canonical model has a single opaque `input`/`output`. Two bad outcomes:
- **Reconstruct**: the normalizer must re-assemble an ordered messages array from string-indexed keys (`.0.`, `.1.`, …), inferring array bounds from key enumeration — fragile, and it duplicates the same content that OpenInference *also* sends verbatim in `input.value`/`output.value`.
- **Passthrough**: land every `llm.input_messages.0.message.content` etc. as its own `attributes` entry. A 10-message conversation becomes ~20+ discrete map keys; `attributes` cardinality scales with conversation length, and no consumer can query "the input" as one value.
Neither is clean. The model has no first-class messages representation, so structured chat transcripts are either lossy-flattened or redundantly duplicated against `input.value`.

### B4 — `input.mime_type` / `output.mime_type` have no canonical home
`input`/`output` are opaque (`02` §4.3) with **no companion content-type field** on the span (unlike `MediaReference.content_type`, which is only for out-of-band media). OpenInference's `input.mime_type`/`output.mime_type` (which tell a consumer whether `input.value` is `text/plain` or `application/json`) can only be dumped into `attributes`. The semantic linkage between the payload and its declared type is lost from the typed surface; a consumer rendering `input` cannot know its MIME type without reaching into the untyped map.

### B5 — `tag.tags.*` are span-level in the wire but `tags` is trace-only
OpenInference attaches `tag.tags.<i>` to **any** span. The canonical `tags` field exists **only on Trace** (`03` §2), not on Span. Tags on a non-root span (e.g. the LLM span) have nowhere promoted to go — they can only be preserved as flattened `attributes` keys (`tag.tags.0`, …) and are **not** union-merged into the trace `tags` set (only root-span promotion feeds trace tags, `03` §3). So span-scoped tags silently fail to participate in trace tag aggregation and remain as indexed attribute keys.

### B6 — Per-message `tool_calls` arrays (array-within-array) have no promoted representation
`llm.output_messages.0.message.tool_calls.0.tool_call.function.name/arguments` is an **indexed array nested inside an indexed array**. There is no promoted "tool_calls" field; the assistant's decision to call `issue_refund` — arguably the single most important output of an agent LLM step — is reconstructable only by walking `output_messages.<m>.tool_calls.<t>.*` and is otherwise a spray of attribute keys. Correlating this tool call to the actual TOOL span (D) that executed it relies on matching `tool_call.id` across spans by convention; the model provides no typed linkage. `arguments` is additionally a JSON string (same string-vs-object issue as B2).

### B7 — Cost has no standard OpenInference attribute
OpenInference defines token counts but **no standard cost attribute**. Any cost the client computed appears only ad-hoc (e.g. nested in `metadata`). Per `06` §2 the wire may write **only** `provided_cost_details`, but there is no deterministic source key to map onto it, so client-provided cost is effectively unreachable for this dialect — it either lands untyped in `attributes.metadata.cost` (never reaching `provided_cost_details`, so `cost_source` can never be `provided`) or is discarded from the cost pipeline. The normalizer cannot honor "provided cost wins" (`06` §4.1) because it can't reliably recognize provided cost.

### B8 — `release`/`version`/`environment` only reachable by unpacking the opaque `metadata` JSON
OpenInference has no standard span attributes for `release` or `version`, and `environment` typically rides in OTLP resource `deployment.environment` or inside the free-form `metadata` JSON string. The canonical `release`/`version` are promoted (`02` §4.2) and `environment` is promoted+frozen. To populate them the normalizer must **parse the opaque `metadata` blob and heuristically pluck keys** — but `metadata` is explicitly a free-form JSON string with no schema. This is guessing: a project that names the key `deploy_sha` instead of `release` gets a null promoted field. Meanwhile the *rest* of `metadata` must still be preserved in `attributes` (invariant 6), so the blob is both partially promoted and wholly preserved — overlapping representations.

### B9 — cache_read overlaps input tokens; provided-map fidelity vs. double-counting
OpenInference reports `llm.token_count.prompt` (total prompt tokens, typically **inclusive** of cached tokens) alongside `prompt_details.cache_read`. Mapping both verbatim into `provided_usage_details` (`input=812`, `cache_read=128`) is faithful to the wire (`06` §2 requires provided values be written as-sent), but the read-time reduction (`06` §6) sums all `input*`-prefixed keys — `cache_read` does **not** start with `input`, so it isn't summed, yet `input` already includes the cached tokens. Whether cached tokens are double-represented depends on provider convention, which the model does not pin down for OpenInference. The well-known key set (`06` §3.1) gives `cache_read`/`cache_write` first-class status but the spec does not state whether `input` is expected to be net-of-cache or inclusive, leaving cost derivation (`06` §4.2, unit-price × usage key) ambiguous for cached tokens.

### B10 — `completion_start_time` (TTFT) has no OpenInference source
The generation field `completion_start_time` (`02` §5) is always null for OpenInference because the dialect has no standard time-to-first-token attribute. Not lossy, but the field is structurally unpopulatable from this dialect — worth noting as a coverage gap.

### B11 — Duplicate representations of tool call input on the TOOL span
Span D carries the same call in three overlapping forms: `input.value` (JSON), `tool_call.function.arguments` (JSON string), and (upstream) `llm.output_messages.0.message.tool_calls.0.tool_call.function.arguments` on span C. `input.value` promotes cleanly to `input`; the `tool_call.function.*` keys have no promoted home and land in `attributes`. The model provides no rule for reconciling these duplicates, so the same argument payload is stored two+ times in different shapes with no canonical linkage.

### B12 — `retrieval.documents.N.*` structured arrays flatten into unqueryable attribute sprawl
A retrieval's result set is inherently a list of `{id, content, score, metadata}` records, flattened by OpenInference into `retrieval.documents.<n>.document.<field>`. There is **no promoted retrieval-documents field and no per-document score primitive**. Consequences:
- The documents (the retriever's actual output) land either reconstructed into opaque `output`, or as `4 × N` discrete `attributes` keys whose count scales with result size.
- The per-document `score` is a genuine relevance measurement but cannot be an LLMObs `Score` entity — Scores attach to a span/trace/session subject (`04`/`07` §4), not to a sub-row of a span. So retrieval scores are demoted to untyped attribute values, invisible to the scoring/aggregation surface.
- `retrieval.documents.N.document.metadata` is JSON-in-a-flattened-array-element: nested opaque structure buried inside a flattened key.
This is the sharpest structural-array mismatch in the dialect.

### B13 — Embedding vectors as attribute values are unbounded blobs
`embedding.embeddings.<i>.embedding.vector` is a high-dimensional float array. With no promoted embedding field, it can only land in `attributes` (invariant 6 forbids dropping it). A single vector is easily 1.5k+ floats; multiple embeddings multiply that. This inflates the `attributes` map with large numeric blobs that are never queryable and that the truncation rules (`02` §7.2) do not cover — §7.2 permits truncating only `input`/`output`, not arbitrary `attributes` values — so there is no sanctioned, observable way to bound them.

### B14 — RERANKER / EVALUATOR collapse to `span`, losing operation semantics
`02` §2.2 maps OpenInference `RERANKER` and `EVALUATOR` to canonical `kind = span` (with `raw_kind` preserved). Both are semantically rich operations: a RERANKER consumes and reorders retrieval documents (a retrieval-family operation); an EVALUATOR produces a judgment that is conceptually a **Score**. Collapsing them to the generic `span` kind means:
- A consumer querying by `kind` cannot distinguish a reranker or evaluator from an arbitrary generic span without parsing `raw_kind` — but `raw_kind` "is never interpreted by the Query API" (`02` §2.1), so these operation types are query-invisible by design.
- An EVALUATOR's output is a measurement that the Score entity exists to hold (`04`), yet the mapping does not route it to a Score; it stays an opaque `span` with its verdict buried in `output`/`attributes`. The two most evaluation-relevant OpenInference kinds are the two that lose the most in normalization.

### B15 — `metadata` (and every OpenInference JSON-string attribute) is doubly opaque
OTLP attribute values are scalars/arrays, so OpenInference serializes structured data (`metadata`, `invocation_parameters`, `document.metadata`, tool `arguments`) as **JSON strings**. The canonical `attributes` map holds JSON scalar/array/object values. Preserving these as strings keeps them non-introspectable (a string, not a map) — but parsing them into objects (as B2/B8 require for promotion) risks failure and re-serialization drift. The model has no consistent policy for "attribute value that is itself a JSON document," so identical structured content is stored as an object in one place (after promotion) and as a string in another (raw preservation), with no equivalence guarantee.
