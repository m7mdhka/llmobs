# Validation — Langfuse OTel

> **Post-review:** the design review ruled on every finding below; the resolutions applied to the spec are tabulated in [`README.md`](README.md#resolutions-applied-post-review-2026-07-09). This worksheet is preserved as the pre-resolution analysis and fixture seed.

**Dialect:** the `langfuse.*` OTel attribute namespace emitted by the Langfuse Python v4 SDK (`langfuse/_client/attributes.py`) and JS v5 SDK, ingested via OTLP by the platform's `OtelIngestionProcessor` / `ObservationTypeMapper`.

**Keys verified on disk** against `packages/shared/src/server/otel/attributes.ts`, `langfuse/_client/attributes.py`, `langfuse/_client/constants.py`, `ObservationType.ts` (enum: `SPAN, GENERATION, EVENT, AGENT, TOOL, CHAIN, RETRIEVER, EVALUATOR, EMBEDDING, GUARDRAIL`), and the processor's level/usage/cost parsing (lines 428–477, 1042–1049, 2341, 2665).

Two facts confirmed on the wire and load-bearing below:
1. `langfuse.observation.usage_details` and `langfuse.observation.cost_details` are emitted as **JSON strings** (`_serialize(...)` = `json.dumps`), and the platform reads them into `providedUsageDetails` / `providedCostDetails` (processor lines 474–477). They are always *client-provided*.
2. `langfuse.observation.level` is a **four-value** enum `DEBUG | DEFAULT | WARNING | ERROR` (`SpanLevel`); the processor defaults it to `DEFAULT`, promotes to `ERROR` only via explicit level or OTel status (lines 428–437).

---

## 1. Representative agent-shaped trace (langfuse.* OTel)

A flat OTLP span stream. `trace_id`/`span_id` are OTel hex; parenting is by OTel `parent_span_id`. The root span carries trace-level attrs (`langfuse.trace.*`).

### Span A — root agent step (`AGENT`)
```
otel.span_id                                = 0a1b2c3d4e5f6071
otel.trace_id                               = 9f8e7d6c5b4a39281706f5e4d3c2b1a0
otel.parent_span_id                         = (none — trace root)
otel.name                                   = "research_agent"
otel.start_time_unix_nano                   = 2026-07-09T10:00:00.000Z
otel.end_time_unix_nano                     = 2026-07-09T10:00:12.400Z
otel.status.code                            = OK
langfuse.observation.type                   = "agent"          # AGENT
langfuse.observation.input                  = "{\"task\":\"summarize Q2 filings\"}"
langfuse.observation.output                 = "{\"answer\":\"...\"}"
langfuse.observation.level                  = "DEFAULT"
langfuse.observation.metadata.retries       = 0                 # flattened metadata
# trace-level (root span carries these):
langfuse.trace.name                         = "research_agent_run"
langfuse.trace.input                         = "{\"task\":\"summarize Q2 filings\"}"
langfuse.trace.output                        = "{\"answer\":\"...\"}"
langfuse.trace.tags                          = "[\"prod\",\"agentic\"]"   # JSON array string
langfuse.trace.metadata.tenant               = "acme"
langfuse.trace.public                        = true
user.id                                      = "user_42"
session.id                                   = "sess_abc"
langfuse.environment                         = "Production"     # deliberately un-sanitized
langfuse.release                             = "2026.07.1"
langfuse.version                             = "v3"
# resource/scope attrs (OTLP):
resource: service.name = "agent-svc", telemetry.sdk.language = "python"
scope:    name = "langfuse-sdk", version = "4.x"
```

### Span B — LLM generation with tool calls (`GENERATION`), child of A
```
otel.span_id                                = 1122334455667788
otel.parent_span_id                         = 0a1b2c3d4e5f6071
otel.name                                   = "planner_llm"
otel.start_time / end_time                  = 10:00:00.100Z / 10:00:03.900Z
otel.status.code                            = OK
langfuse.observation.type                   = "generation"     # GENERATION
langfuse.observation.model.name             = "gpt-4o-2024-08-06"
langfuse.observation.model.parameters       = "{\"temperature\":0.2,\"max_tokens\":1024}"   # JSON string
langfuse.observation.input                  = "[{\"role\":\"user\",\"content\":\"...\"}]"
langfuse.observation.output                 = "{\"tool_calls\":[{\"name\":\"search\",\"args\":{...}}]}"
langfuse.observation.completion_start_time  = "2026-07-09T10:00:00.850Z"   # JSON-serialized ts
langfuse.observation.usage_details          = "{\"input\":812,\"output\":143,\"total\":955,\"input_cached_tokens\":128}"  # JSON string
langfuse.observation.cost_details           = "{\"input\":0.00203,\"output\":0.00214,\"total\":0.00417}"                    # JSON string
langfuse.observation.prompt.name            = "planner_system"
langfuse.observation.prompt.version         = 7
langfuse.observation.level                  = "DEFAULT"
langfuse.version                            = "v3"
```

### Span C — tool execution (`TOOL`), child of A
```
otel.span_id                                = 2233445566778899
otel.parent_span_id                         = 0a1b2c3d4e5f6071
otel.name                                   = "search_tool"
langfuse.observation.type                   = "tool"           # TOOL
langfuse.observation.input                  = "{\"query\":\"Q2 filings\"}"
langfuse.observation.output                 = "{\"hits\":12}"
langfuse.observation.level                  = "WARNING"        # rate-limit warning
langfuse.observation.status_message         = "provider throttled; retried"
```

### Span D — retrieval (`RETRIEVER`), child of C
```
otel.span_id                                = 33445566778899aa
otel.parent_span_id                         = 2233445566778899
otel.name                                   = "vector_search"
langfuse.observation.type                   = "retriever"      # RETRIEVER
langfuse.observation.input                  = "{\"embedding_query\":\"...\"}"
langfuse.observation.output                 = "[{\"doc_id\":\"d1\",\"score\":0.82}]"
langfuse.observation.level                  = "DEBUG"          # diagnostic
langfuse.observation.metadata.index         = "filings-v2"
```

---

## 2. Field-by-field mapping table

Legend for target column: **[P]** promoted span/trace field · **[A]** lands in `attributes` map · **[R]** kernel reserved `llmobs.raw.*` / `llmobs.dq.*` · **[ref]** reference object.

### 2a. Span-level observation attributes → canonical Span

| Source attribute / signal | Canonical field | attributes-map key | raw-preserved | Notes |
|---|---|---|---|---|
| OTel `span_id` | `id` [P] | — | — | Hex `span_id` becomes `id` (02 §4, `01-entities` §3). Frozen. |
| OTel `trace_id` | `trace_id` [P] | — | — | Frozen. |
| OTel `parent_span_id` | `parent_span_id` [P] | — | — | `null` on root span A. |
| OTLP resource `project`/API key context | `project_id` [P] | — | — | Tenant scope resolved from the OTLP auth/export context, not a span attr. Frozen. |
| OTel span `name` | `name` [P] | — | — | Human-readable op name. |
| OTel `start_time_unix_nano` | `start_time` [P] | — | `llmobs.raw.start_time` if a later update conflicts | Frozen merge anchor (05 §5). |
| OTel `end_time_unix_nano` | `end_time` [P] | — | — | Absent ⇒ `point_event`/open. `EVENT`-typed span D-style with no end ⇒ `point_event`. |
| OTel `status.code` (`OK`/`ERROR`/`UNSET`) | `status.code` [P] | — | — | Direct tri-state map (02 §4.1). |
| `langfuse.observation.status_message` (span C) | `status.message` [P] | — | — | Free text. |
| `langfuse.observation.level` (`DEBUG/DEFAULT/WARNING/ERROR`) | `status.code`: `ERROR`→`error`, else `unset` [P] | — | **`llmobs.raw.level`** [R] | **Lossy demotion — see Design bugs.** `DEBUG`/`DEFAULT`/`WARNING` all collapse to `unset`; original preserved in `llmobs.raw.level` (02 §4.1, 08 §4). |
| `langfuse.observation.type` = `agent` (A) | `kind = agent_step` [P] | — | `raw_kind = "AGENT"` | 02 §2.2. |
| `langfuse.observation.type` = `generation` (B) | `kind = generation` [P] | — | `raw_kind = "GENERATION"` | Triggers `generation_shaped`. |
| `langfuse.observation.type` = `tool` (C) | `kind = tool_call` [P] | — | `raw_kind = "TOOL"` | |
| `langfuse.observation.type` = `retriever` (D) | `kind = retrieval` [P] | — | `raw_kind = "RETRIEVER"` | |
| `langfuse.observation.type` = `embedding` | `kind = embedding` [P] | — | `raw_kind = "EMBEDDING"` | |
| `langfuse.observation.type` = `guardrail` | `kind = guardrail` [P] | — | `raw_kind = "GUARDRAIL"` | |
| `langfuse.observation.type` = `chain` | `kind = agent_step` **or** `span` [P] | — | `raw_kind = "CHAIN"` | **Ambiguous — see Design bugs.** 02 §2.2: `agent_step` if it orchestrates sub-steps else `span`; normalizer has no signal to decide. |
| `langfuse.observation.type` = `evaluator` | `kind = span` [P] | — | `raw_kind = "EVALUATOR"` | **Lossy — see Design bugs.** Evaluator semantics survive only in `raw_kind`; may also fire `llmobs.dq.unmapped_kind`? No — it has an explicit table entry, so no DQ flag, but the eval intent is flattened. |
| `langfuse.observation.type` = `span` | `kind = span` [P] | — | `raw_kind = "SPAN"` | |
| `langfuse.observation.type` = `event` | `kind = span`, recorded as `point_event` [P] | — | `raw_kind = "EVENT"` | No `end_time`. |
| (any unlisted future type) | `kind = span` [P] | — | `raw_kind = <value>`; `llmobs.dq.unmapped_kind = true` [R] | 08 §3 fallback. |
| `langfuse.observation.input` | `input` (opaque) [P] | — | — | Opaque passthrough, no schema validation (02 §4.3). May carry `@@@llmobsMedia:…@@@` tokens. |
| `langfuse.observation.output` | `output` (opaque) [P] | — | — | Same. |
| `langfuse.environment` = `"Production"` | `environment` [P] (sanitized → `"production"`) | — | **`llmobs.raw.environment`** = `"Production"`; `llmobs.dq.dimension_coerced.environment = true` [R] | Sanitized in shared `normalize` stage (08 §2): lowercase → `"production"` differs from offered ⇒ raw preserved + DQ counter. Frozen. |
| `langfuse.release` | `release` [P] | — | — | Span mirrors trace release. |
| `langfuse.version` | `version` [P] | — | — | Per-span logic/prompt version. |
| `user.id` (and legacy `langfuse.user.id`) | `user_id` [P] | — | — | Both aliases resolve to same field. On a non-root span, this is a span dimension; on root A it also promotes to trace `user_id` (03 §3). |
| `session.id` (and legacy `langfuse.session.id`) | `session_id` [P] | — | — | Same alias/promotion behavior. |
| `langfuse.observation.metadata.<k>` (flattened, e.g. `.retries`, `.index`) | — | `langfuse.observation.metadata.<k>` [A] | — | Non-promoted; preserved verbatim in `attributes` (02 §6). SDK flattens nested metadata into dotted keys on the wire. |
| OTLP resource attrs (`service.name`, `telemetry.sdk.language`) | — | as-is keys [A] | — | Resource attrs preserved in `attributes` (02 §6, invariant 6). |
| OTLP scope attrs (`scope.name=langfuse-sdk`, version) | — | as-is keys [A] | — | Scope attrs preserved (02 §6). |
| `langfuse.internal.as_root` / `langfuse.internal.is_app_root` | — | as-is keys [A] | — | SDK-internal routing flags; no canonical home → `attributes`. Not interpreted. |

### 2b. Generation-only attributes (Span B) → generation fields

| Source attribute | Canonical field | Notes |
|---|---|---|
| `langfuse.observation.model.name` | `model` [P-gen] | Client-provided model string. |
| `langfuse.observation.model.parameters` (JSON string) | `model_parameters` [P-gen] | Parsed from JSON string to opaque map. |
| `langfuse.observation.completion_start_time` | `completion_start_time` [P-gen] | Time-to-first-token. |
| `langfuse.observation.usage_details` (JSON string) | **`provided_usage_details`** [P-gen] | **Only** the provided map is wire-writable (06 §2). Keys normalized: `input`/`output`/`total` are already well-known; `input_cached_tokens` → SHOULD map to `cache_read` (06 §3.1) — normalizer's responsibility. `usage_details` (resolved) is filled by kernel enrichment, never from wire. |
| `langfuse.observation.cost_details` (JSON string) | **`provided_cost_details`** [P-gen] | Wire cost is client-provided ⇒ **`cost_source = "provided"`**, `cost_details` = copy, derivation short-circuited, `pricing_snapshot_ref = null` (06 §4.1). See Design bugs. |
| (kernel enrichment output) | `usage_details`, `cost_details`, `total_cost`, `cost_source`, `pricing_snapshot_ref` | Never on the wire; produced by enrichment (06 §2–5). With provided cost present here, `total_cost = 0.00417`, `cost_source = provided`, `pricing_snapshot_ref = null`. |
| `langfuse.observation.prompt.name` = `planner_system` **+** `langfuse.observation.prompt.version` = `7` | **`prompt_ref`** [ref] | Two attrs → single `(type="prompt", id, label?)` reference (05.4, 07 §1). See Design bugs. `label` snapshot ≈ `"planner_system@v7"`. Not promoted/queryable. |

### 2c. Trace-level attributes (from root span A) → canonical Trace

| Source attribute | Canonical field | attributes-map key | Notes |
|---|---|---|---|
| OTel `trace_id` | Trace `id` [P] | — | Frozen. |
| (auth context) | Trace `project_id` [P] | — | Frozen. |
| `langfuse.trace.name` | Trace `name` [P] | — | 03 §1. |
| `langfuse.trace.input` | Trace `input` (opaque) [P] | — | Independent of span input. |
| `langfuse.trace.output` | Trace `output` (opaque) [P] | — | |
| `langfuse.trace.tags` (JSON array string) | Trace `tags` [P] | — | Parsed to `array<string>`; union-merged (03 §2). |
| `langfuse.trace.metadata.<k>` (e.g. `.tenant`) | — | `langfuse.trace.metadata.<k>` [A] | Langfuse `metadata` folds into `attributes` (03 §1 evidence). |
| `langfuse.trace.public` = `true` | **none** | `langfuse.trace.public` [A] | **UI-only flag, no canonical home — see Design bugs.** Model drops `public`/`bookmarked` (03 §1); to satisfy invariant 6 it must still land in `attributes`. |
| `user.id` / `langfuse.user.id` | Trace `user_id` [P] | — | Promoted from root span (03 §3). |
| `session.id` / `langfuse.session.id` | Trace `session_id` [P] | — | Same. |
| `langfuse.environment` | Trace `environment` [P] (sanitized) | `llmobs.raw.environment` if coerced | Frozen; same sanitization as span. |
| `langfuse.release` | Trace `release` [P] | — | |
| `langfuse.version` | Trace `version` [P] | — | |
| OTel span times | Trace `start_time` [P] (frozen); `end_time` derived `max(span.end_time)` | — | 03 §3. |
| OTel root `status` | Trace `status` [P] | — | Independent of span status. |

### 2d. Canonical fields with no Langfuse-OTel source (coverage completeness)

| Canonical field | How populated for this dialect |
|---|---|
| Span `attributes` (default `{}`) | Accumulates all [A] rows above; never null. |
| Span `usage_details`/`cost_details`/`total_cost`/`cost_source`/`pricing_snapshot_ref` | Kernel enrichment only (06 §2). |
| Span `provided_usage_details`/`provided_cost_details` (default `{}`) | Empty on non-generation spans A/C/D. |
| Trace `end_time` | Adapter-derived (03 §3); not on wire. |
| Media `MediaReference` entity | Only if `input`/`output` contained `@@@llmobsMedia:…@@@` tokens (02 §7); Langfuse emits `@@@langfuseMedia:…@@@` which the compat normalizer must rewrite to the LLMObs token form. |

---

## Design bugs / unclean landings

**1. `langfuse.observation.level` four-state → tri-state `status` loses DEBUG and WARNING.**
Langfuse `level ∈ {DEBUG, DEFAULT, WARNING, ERROR}` (confirmed `SpanLevel`; processor lines 428–437 default to `DEFAULT`). Canonical `status.code ∈ {unset, ok, error}` (02 §4.1). Only `ERROR` maps to `error`; `DEBUG`, `DEFAULT`, and `WARNING` all collapse to `unset` and are indistinguishable in the promoted, queryable surface. Span D's `DEBUG` and span C's `WARNING` survive **only** as `llmobs.raw.level` in `attributes` (08 §4) — not queryable, not aggregatable. Why it's a problem: a WARNING is a real operational signal (span C was throttled) that operators will want to filter/alert on; demoting it to an opaque raw attribute means severity-based queries silently miss it. This is a genuine model-vs-source impedance mismatch, correctly flagged rather than papered over: the spec deliberately chose OTel tri-state and accepts the loss. Do **not** invent a fourth status value or overload `status.message` to smuggle severity.

**2. `CHAIN` maps to two possible kinds with no disambiguating signal.** 02 §2.2 says Langfuse `CHAIN` → `agent_step` if it orchestrates sub-steps, else `span`. The langfuse.* wire carries **no** attribute expressing "orchestrates sub-steps." The normalizer would have to inspect whether the span has children — but in a flat OTLP stream children may not have arrived yet at normalize time (spans are independent events, 03 §3). Result: the kind decision is either non-deterministic (depends on arrival order/buffering) or defaults to `span`, under-classifying real orchestration steps as generic spans. Why it's a problem: `kind` is **frozen after first write** (02 §2), so a wrong early guess cannot be corrected when children later arrive. The clean position is to default `CHAIN → span` (never `agent_step`) unless a same-event signal exists, accepting under-classification, rather than a stateful lookahead that violates freeze semantics. Flagged, not hacked.

**3. `EVALUATOR` (and `EVENT`) flatten to generic kinds, losing taxonomy in the promoted surface.** `EVALUATOR → span` and `EVENT → span (point_event)` per 02 §2.2. Langfuse's own enum treats EVALUATOR as first-class (it drives eval dashboards), but the canonical seven-kind enum has no evaluator kind, so the semantics live only in `raw_kind` (which "is never interpreted by the Query API," 02 §2.1). Why it's a problem: an evaluator observation and a plain span become query-indistinguishable except via a non-indexed raw string. This is the deliberate LM-1 trade (small closed enum vs. foreign taxonomy) and is correctly surfaced as a known lossy landing, not worked around by expanding the enum.

**4. `usage_details` / `cost_details` arrive as JSON strings and force `cost_source = provided`.** Both are `json.dumps` strings on the wire (`attributes.py` `_serialize`), read by the platform into `providedUsageDetails`/`providedCostDetails` (processor 474–477). Per 06 §2 the wire may write **only** the `provided_*` maps; per 06 §4.1 a non-empty `provided_cost_details` **short-circuits all kernel derivation**, forcing `cost_source = "provided"` and `pricing_snapshot_ref = null`. Why it's a problem: Langfuse SDKs frequently emit `cost_details` computed by the *client's own* (possibly stale or wrong) price table, yet that client cost now wins over the kernel's authoritative pricing and blocks re-derivation — the span becomes un-re-priceable (`pricing_snapshot_ref` is null, so the re-pricing job in 06 §5 has nothing to target). The `cost_source` field at least makes provided-vs-derived distinguishable after the fact (which Langfuse itself cannot do), but the precedence itself means client cost errors are permanently authoritative. Correct behavior per spec; flagged as a real provenance hazard, not overridden.

**5. `prompt.name` + `prompt.version` (two attrs) collapse into one `prompt_ref` and leave the promoted set.** LM-1 lists prompt linkage as part of `generation_shaped`, but LM-2's promoted set omits prompt fields (02 §5.4). The two wire attrs become a single `(type="prompt", id, label?)` reference (07 §1) — not a promoted, queryable scalar. Why it's a problem for this dialect: Langfuse users routinely **filter and group generations by prompt name/version** (it's a core Langfuse UI capability), but under this model prompt is a plugin concern and the reference is a soft, possibly-dangling pointer (07 §3) — so "show me all generations using planner_system@v7" is no longer a first-class promoted query. Additionally, `prompt.version` is an integer on the wire but must be encoded into the reference `id`/`label` string; there is no separate typed version field. This is the deliberate LM-12 resolution (prompt as reference, not promoted column); flagged as a queryability regression relative to source, correctly not resolved by promoting prompt fields.

**6. `langfuse.trace.public` has nowhere to go but `attributes`.** The model explicitly drops UI-only flags `public`/`bookmarked` (03 §1, routing them to a plugin `kv` store conceptually). But invariant 6 forbids losing any ingested attribute, so `langfuse.trace.public = true` must still be preserved verbatim in the trace `attributes` map under its raw key. Why it's a problem: it lands as a dead attribute — the kernel assigns it no meaning, no plugin is guaranteed to read it, and it is neither the intended `kv` home nor a queryable field. It is preserved (satisfying invariant 6) but semantically orphaned. The clean stance is exactly this — keep it in `attributes` untouched rather than inventing a promoted `public` column — but it is an unclean landing worth flagging: a genuine source flag with no real destination in the canonical model.

**7. Environment normalization is the one place the source is silently lossy and the model deliberately diverges.** `langfuse.environment = "Production"` sanitizes to `"production"` (08 §2). Langfuse's OTLP path does **no** normalization (study Ch. 05 §8; 08 §2 evidence), so upstream the same value is stored with divergent casing per transport. The canonical model fixes this by running one sanitization in the shared `normalize` stage and, crucially, making the coercion observable via `llmobs.raw.environment` + `llmobs.dq.dimension_coerced`. Not a bug in the mapping — rather the mapping exposes a real Langfuse defect; noted here because the LLMObs landing for `langfuse.environment` intentionally differs (byte-for-byte) from what Langfuse itself stores, which round-trip/parity tests must expect.
