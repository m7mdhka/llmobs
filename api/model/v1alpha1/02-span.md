# Span (`v1alpha1`)

**Section type:** Normative except where marked *Informative*.

A **Span** is one unit of work inside a trace. It is the workhorse entity: an LLM
generation, a tool call, a retrieval, an agent step, a guardrail check, a generic
span, or a point-in-time event are all spans, distinguished by `kind` (§2) and
constrained by a **payload shape** (§3).

## 1. The promoted field set (Normative) — LM-2

The **promoted set** is the closed list of first-class, typed, queryable span
fields. It is exactly:

`trace_id, kind, name, start_time, end_time, status, environment, release,
version, session_id, user_id`

plus, on `generation_shaped` spans only, the `model`, `provider`, and usage/cost
fields defined in §5 and `06-usage-cost.md`. (`provider` was added to the promoted
set by design finding F7; see ADR-0018 addendum.)

Every other attribute — no matter how it arrived — MUST live in the `attributes`
map (§6). Raw ingested attributes are ALWAYS preserved there (invariant 6).

### 1.1 Promotion is a near-permanent commitment (Normative)

Adding a field to the promoted set is an additive change that nonetheless
REQUIRES an ADR (`00-overview.md` §6). Rationale, carried from the study:

> Evidence: Langfuse retrofitted `session_id`/`user_id` indexes after table
> creation (`0005`, `0006`), and its aggregation queries only count rows where a
> promoted attribute was set at write time — there is **no** retroactive backfill
> (study Ch. 08; digest §2). A promoted field therefore behaves as a semi-permanent
> commitment: adding one later does not repair historical rows. The promoted set is
> deliberately small; the long tail lives in `attributes`.

## 2. Kind (Normative) — LM-1

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

- `kind` is **frozen** after first write (§`05-update-semantics.md` §3).
- New kinds are **additive-only** within a major (`00-overview.md` §6); a
  consumer encountering an unknown kind MUST treat it as `span` (forward
  compatibility) rather than error.
- A span of any kind MAY be recorded as a `point_event` (no `end_time`) when it
  represents an instantaneous occurrence (§3.1).

### 2.1 `raw_kind` (Normative)

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

### 2.2 Foreign-type mapping policy (Normative)

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
`kind` is frozen (§`05-update-semantics.md` §3) — a stateful guess would raise a
`frozen_field_conflict` for a purely structural reason.

### 2.3 `CHAIN` maps to `span` (Normative)

`CHAIN` (OpenInference, Langfuse) MUST map to `kind = span`, never `agent_step`.
"Does it orchestrate sub-steps" is not evaluable from the span alone, and a
speculative `agent_step` would pollute agent analytics with every trivial
sequence/`RunnableSequence`. `raw_kind = "chain"` keeps chains queryable
(via reference/attribute filtering, §`07-references.md`) without expanding the
frozen enum. This resolves the ambiguity LM-1 originally left open (design
finding F1).

### 2.4 Evaluators map to `span`; normalizers MUST NOT synthesize scores (Normative)

`EVALUATOR`/`RERANKER` map to `kind = span` with `raw_kind` preserved. An
evaluator's *execution* is a span (it has latency, cost, and its own child LLM
calls); its *result* is a **Score** emitted through the score write path by
whoever ran it, with verified `source` and subject (`04-score.md`).

A normalizer MUST NOT synthesize Score entities from span attributes. A normalizer
cannot verify a score's `source` (human vs llm_judge vs code) or its subject, so
inventing scores from spans would fabricate provenance. Scores enter only through
the score write path (design finding Q3).

## 3. Payload shapes (Normative) — LM-1

The model defines **three** nested payload shapes. Each is a strict superset of
the previous. A span's `kind` declares its **default** shape (§2); a span MAY be
recorded at a *narrower* shape than its kind's default (e.g. a `generation`
recorded as a `point_event`) but MUST NOT be recorded at a *wider* shape than
`generation_shaped`.

```
point_event  ⊂  timed_span  ⊂  generation_shaped
```

### 3.1 `point_event` (Normative)

An instantaneous occurrence. Fields: the full promoted set **except** `end_time`
and the generation fields. `end_time` MUST be absent (or equal to `start_time`).
A `point_event` MUST NOT carry any generation field (§5); if one is present the
span is not a `point_event`.

### 3.2 `timed_span` (Normative)

A `point_event` plus `end_time` (a `timed_span` MAY still be open — `end_time`
absent — until a later update closes it). A `timed_span` MUST NOT carry
generation fields.

### 3.3 `generation_shaped` (Normative)

A `timed_span` plus the generation fields (§5): `model`, `provider`,
`model_parameters`, `completion_start_time`, the usage/cost fields
(`06-usage-cost.md`), and the OPTIONAL `prompt_ref` (§5.4). A span MUST be
`generation_shaped` if and only if it carries any generation field. Only `generation` and `embedding` kinds default
to this shape, but any kind MAY carry generation fields if the instrumentation
does (`raw_kind` preserves the original semantics).

> Evidence: Langfuse defines exactly this inheritance — `EVENT` (no `endTime`) ⊂
> `SPAN` (adds `endTime`) ⊂ `GENERATION` (adds model/usage/cost/prompt) — and wires
> all seven "rich" subtypes (AGENT/TOOL/CHAIN/…) to the generation body so any
> subtype may carry usage/cost (study Ch. 06 §3; Ch. 05 §2). LM-1 formalizes the
> three shapes and decouples them from `kind`, so a `tool_call` that reports token
> usage is representable without forcing its kind to `generation`.

## 4. Field reference — promoted fields (Normative)

Types below are logical. `timestamp` is an instant with millisecond precision or
finer, serialized as RFC 3339 UTC. `string` is UTF-8.

| Field | Type | Null? | Shape | Frozen? | Semantics |
|---|---|---|---|---|---|
| `id` | string | no | all | **yes** | Span identity; OTel `span_id` hex where present (`01-entities.md` §3). |
| `project_id` | string | no | all | **yes** | Tenant scope. |
| `trace_id` | string | no | all | **yes** | Owning trace (`03-trace.md`). |
| `parent_span_id` | string | yes | all | no | In-trace parent; `null` for a trace-root span. |
| `kind` | enum | no | all | **yes** | §2. |
| `raw_kind` | string | yes | all | no | §2.1. |
| `name` | string | no | all | no | Human-readable operation name. |
| `start_time` | timestamp | no | all | **yes** | Span start; also the merge/identity anchor (§`05`). |
| `end_time` | timestamp | yes | `timed_span`+ | no | Span end; absent ⇒ open or `point_event`. |
| `status` | object | no | all | no | §4.1. Defaults to `{code: "unset"}`. |
| `environment` | string | no | all | **yes** | §4.2. Defaults to `"default"`. |
| `release` | string | yes | all | no | §4.2. |
| `version` | string | yes | all | no | §4.2. |
| `session_id` | string | yes | all | no | Dimension (`01-entities.md` §4.1). |
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

### 4.1 Status model (Normative)

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
  (`08-data-quality.md` §4), which keeps DEBUG/WARNING filterable.

> Under-specification resolved (flagged for review): LM-2 promotes `status` but does
> not define its shape or the fate of Langfuse's four-value `level`
> (DEBUG/DEFAULT/WARNING/ERROR, study Ch. 06 §3). This spec chooses an OTel
> tri-state status (`unset|ok|error`) as canonical and demotes non-error severity to
> a preserved attribute. See the final report's "under-specified" list.

### 4.2 Dimensions: environment, release, version (Normative) — LM-11

- **`environment`** — a sanitized, low-cardinality, project-scoped string;
  defaults to `"default"`; **frozen** after first write. Sanitization
  (lowercase, reserved-prefix strip, length cap, charset) is defined once in
  `08-data-quality.md` §2 and MUST be executed in the `normalize` middleware
  stage for **every** transport, including compat plugins. Coercion to `"default"`
  MUST NOT happen silently: the offered value MUST be preserved in `attributes`
  under `llmobs.raw.environment` and the data-quality counter incremented
  (`08-data-quality.md`).
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

### 4.3 `input` / `output` (Normative) — LM-10

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

### 4.4 Span events (Normative) — Q1

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
- `events` is **union-merged** on update (§`05-update-semantics.md` §3.1): a later
  event set adds events; identical events (same `name`, `timestamp`, and
  `attributes`) are deduplicated, so re-delivery is idempotent.

> Rationale: OTel span events carry their own timestamps and ordering that opaque
> `input`/`output` cannot preserve, and every framework models chat messages
> differently (design finding Q1 across OTel GenAI, OpenInference, OpenLLMetry). A
> generic `(name, timestamp, attributes)` record captures the native OTel shape
> without committing the model to any one message schema.

## 5. Generation fields (Normative)

Present only on `generation_shaped` spans (§3.3). Detailed usage/cost semantics
are in `06-usage-cost.md`; this section fixes the fields.

| Field | Type | Null? | Semantics |
|---|---|---|---|
| `model` | string | yes | The **resolved/served** model — the model that actually ran (e.g. OTel `gen_ai.response.model`, `gpt-4o-2024-08-06`). The requested model (e.g. `gen_ai.request.model`, `gpt-4o`), when it differs, is preserved under `llmobs.raw.model_requested`. (F7) |
| `provider` | string | yes | The model provider (e.g. `openai`, `anthropic`, `bedrock`). Promoted so cost derivation and analytics have `(provider, model)` as typed fields, not attribute lookups. (F7) |
| `model_parameters` | map<string, string> | yes | Request parameters as **verbatim strings**, keyed by name. The kernel does **not** parse them at normalize time (opaque rule, F2). Well-known keys: `temperature`, `max_tokens`, `top_p`, `top_k`, `frequency_penalty`, `presence_penalty`, `stop_sequences`, `seed`. Any additional key is permitted. |
| `completion_start_time` | timestamp | yes | Time to first token, for streaming generations. |
| `provided_usage_details` | map<string, integer≥0> | no | Usage as sent by the client. Defaults to `{}`. (`06` §2) |
| `usage_details` | map<string, integer≥0> | no | Resolved usage. Defaults to `{}`. (`06` §3) |
| `provided_cost_details` | map<string, decimal> | no | Cost as sent by the client. Defaults to `{}`. (`06` §2) |
| `cost_details` | map<string, decimal> | no | Resolved cost. Defaults to `{}`. (`06` §3) |
| `total_cost` | decimal | yes | Scalar total cost (`06` §4). |
| `cost_source` | enum `provided`\|`derived` | yes | How `cost_details` was obtained (`06` §4). |
| `pricing_snapshot_ref` | reference | yes | Reference to the price entry used for derivation (`06` §5, `07-references.md`). |
| `prompt_ref` | reference | yes | §5.4. |

### 5.4 `prompt_ref` (Normative)

`prompt_ref` is an OPTIONAL `(type, id, label?)` reference (`07-references.md`) to
a prompt owned by the prompt-management plugin. It is **not** a promoted,
queryable scalar and does **not** expand the promoted set; prompts are a plugin
concept (invariant 2). A normalizer that receives prompt linkage (e.g. Langfuse
`prompt_name`/`prompt_version`) MUST record it as a `prompt_ref` with a label
snapshot rather than as promoted columns.

> Under-specification resolved (flagged): LM-1 lists "prompt-linkage" as part of the
> `generation_shaped` shape, but LM-2's promoted set omits prompt fields. This spec
> resolves the tension by representing prompt linkage as a **reference** (LM-12),
> keeping prompts a plugin concern (D2). See the final report.

## 6. The attributes map (Normative)

`attributes` is an open `map<string, value>` where `value` is a JSON scalar,
array, or object. It holds every non-promoted attribute. Rules:

- A normalizer MUST place every source attribute it does not map to a promoted
  field into `attributes`, unchanged, so **no ingested attribute is ever lost**
  (invariant 6). This includes OTel resource and scope attributes.
- Keys under the reserved namespace **`llmobs.*`** are owned by the kernel and
  carry canonical-preserved values (`llmobs.raw.*`, data-quality signals
  `llmobs.dq.*` — `08-data-quality.md`). Normalizers and plugins MUST NOT write
  arbitrary keys under `llmobs.*`.
- The map is deep-merged on update (§`05-update-semantics.md` §2).

> Evidence: Langfuse preserves unrecognized OTLP span/resource/scope attributes under
> `metadata.attributes` / `.resourceAttributes` / `.scope` (study Ch. 05 §5). This
> model requires the same total preservation but under a single `attributes` map with
> a reserved `llmobs.*` namespace for kernel-owned keys.

### 6.1 Well-known attribute conventions (Normative) — Q2, F4

Some cross-span relationships that are not promoted fields are carried by
**well-known attribute keys**. These are documented conventions, not schema — a
consumer that understands them gets extra power; one that does not still sees
plain attributes.

| Well-known key | Meaning |
|---|---|
| `llmobs.call_id` | Correlates a generation's emitted tool call to the `tool_call` span that executed it. A `generation` span sets `llmobs.call_id` (or one per parallel call) to the provider tool-call id; the corresponding `tool_call` span sets the same `llmobs.call_id`. This gives queryable generation↔tool-call linkage without a typed field (design finding Q2). |
| `error.type` | The error class/type when a span failed (e.g. `TimeoutError`). Preserved verbatim; does not by itself set `status.code = "error"` (§4.1, F4). |
| `llmobs.raw.*`, `llmobs.dq.*` | Kernel-reserved (`08-data-quality.md`). |

Per-document retrieval relevance (e.g. `retrieval.documents.N.document.score`)
stays in the `output` payload; those are payload details of a retrieval, not
project-level measurements, so they are **not** Score entities (design finding
Q2, see `04-score.md`).

### 6.2 Attribute value size cap (Normative) — F5

An adapter MUST enforce a per-attribute-value size cap. The cap is **configurable**
with a default of **16 KB** (serialized-UTF-8 bytes). When an attribute value
exceeds the cap, the adapter MUST truncate that value and stamp the span with the
data-quality signal `llmobs.dq.truncated_attributes = true`
(`08-data-quality.md` §3), incrementing the counter. Large payloads (embedding
vectors, big blobs) SHOULD be sent as **media references** (§7) rather than inline
attribute values. The cap MUST NOT apply to promoted or identity/frozen fields.

## 7. Media references (Normative) — LM-10

Binary/media payloads are handled **out of band**. A span field (`input`,
`output`, or an `attributes` value) MAY contain one or more **media reference
tokens** in place of inline bytes.

### 7.1 Token format (Normative)

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

### 7.2 Oversized-payload truncation (Normative)

An adapter MAY truncate an oversized `input`/`output` payload **only** if it
stamps the span with the data-quality signal `llmobs.dq.truncated = true`
(`08-data-quality.md`) and increments the truncation counter. **Silent truncation
is prohibited.** An adapter MUST NOT truncate any promoted field or any
identity/frozen field.

> Evidence: Langfuse's `ClickhouseWriter` truncates oversized fields on a size error
> (once) with no per-row marker (study Ch. 04 §10). LM-10 diverges: truncation is
> permitted but MUST be observable via a stamped flag and a counter — never silent.
