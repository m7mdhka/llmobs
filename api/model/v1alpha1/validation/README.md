# Validation Pass (`v1alpha1`)

**Section type:** Informative. This directory holds the field-by-field mapping
worksheets produced by driving one representative **agent-shaped trace** (nested
agent step + LLM generation with tool calls + tool execution + retrieval +
usage/cost) through each of four ingestion dialects and mapping every source
attribute onto the canonical model. These worksheets seed the normalizer fixtures
(`kernel/testdata/fixtures/`).

| Dialect | Worksheet |
|---|---|
| Official OpenTelemetry GenAI (`gen_ai.*`) | [`otel-genai.md`](otel-genai.md) |
| OpenInference (Arize) | [`openinference.md`](openinference.md) |
| OpenLLMetry (Traceloop) | [`openllmetry.md`](openllmetry.md) |
| Langfuse OTel (`langfuse.*`) | [`langfuse-otel.md`](langfuse-otel.md) |

## Outcome (Informative)

**No source field is ever dropped.** Every attribute in every dialect lands
somewhere — a promoted field, the `attributes` map, or a reserved `llmobs.raw.*`
key — so invariant 6 (raw attributes always preserved) holds across all four
dialects. In that sense the model is complete: the mappings did not force any
"hack around a lost field."

**But the pass surfaced structural mismatches and under-specifications** where a
field *lands* but not *cleanly* — it flattens into attribute sprawl, requires a
non-deterministic normalizer decision, or exposes a rule the spec left open.
These are recorded below and, per the task's ground rule, are **not** worked
around in the mappings; they are flagged for a human decision before the model is
frozen. None of the twelve locked decisions (LM-1..LM-12) was re-litigated in the
mappings.

## Consolidated findings (Informative)

Legend: **[FIX]** a concrete spec defect to correct · **[TIGHTEN]** an
under-specification to pin down · **[CONFIRM]** a deliberate trade working as
intended, surfaced for confirmation.

| # | Class | Finding | Dialects | Where |
|---|---|---|---|---|
| F1 | **[FIX]** | `CHAIN → agent_step OR span` is non-deterministic: the "orchestrates sub-steps" predicate is not evaluable from the span alone at normalize time, and `kind` is **frozen**, so an early wrong guess raises a `frozen_field_conflict` for a structural reason. LM-1 itself encodes the ambiguity ("CHAIN → agent_step or span"). Needs a deterministic rule. | OpenInference, Langfuse | `02-span.md` §2.2 |
| F2 | **[TIGHTEN]** | `model_parameters` membership is undefined: which `gen_ai.request.*` keys fold into `model_parameters` vs stay in `attributes`, and the source is a JSON *string* while the field is an *object* (parse asymmetry; no fallback slot on parse failure). | OTel GenAI, OpenInference | `02-span.md` §5 |
| F3 | **[TIGHTEN]** | Usage `input` net-vs-inclusive-of-cache is unspecified: is `input` expected to include `cache_read` tokens or be net of them? This changes cost derivation (`usage × unit price`). | OpenInference, OpenLLMetry | `06-usage-cost.md` §3 |
| F4 | **[TIGHTEN]** | No rule for `error.type` / `gen_ai.response.finish_reasons` → `status`: a content-filtered/length-truncated generation, or a span with `error.type` set but OTLP `status=OK`, currently reads as success; severity hides in `attributes`. | OTel GenAI | `02-span.md` §4.1 |
| F5 | **[TIGHTEN]** | Large `attributes` values (embedding vectors, big JSON) have no sanctioned bound: §7.2 truncation covers only `input`/`output`, so vectors bloat `attributes` with no observable truncation path. | OpenInference | `02-span.md` §7.2 |
| F6 | **[TIGHTEN]** | `input`/`output` have no content-type companion: OpenInference `input.mime_type` (is `input.value` text or JSON?) can only land untyped in `attributes`; the payload/type linkage is lost from the typed surface. | OpenInference | `02-span.md` §4.3 |
| F7 | **[TIGHTEN]** | Cost-derivation inputs (`gen_ai.response.model`, `gen_ai.provider.name`) are not promoted; enrichment must read `attributes` string keys rather than typed fields, and `model` captures only the *request* model, not the served one. | OTel GenAI | `06-usage-cost.md` §4 |
| Q1 | **[CONFIRM]** | **Span events / structured messages have no first-class home.** OTel span *events* (`gen_ai.system.message`, `gen_ai.choice` — timestamped records on a span) and flattened message/tool-call/retrieval-document arrays fold into opaque `input`/`output` (losing per-event timestamps) or spray across `attributes`. When both the event and attribute encodings arrive, there is no precedence rule. Deliberate per LM-2/LM-10 (small promoted set, opaque payloads) — but the mission's `01-entities` sketch named "span (+ span events)". | OTel GenAI, OpenInference, OpenLLMetry | `01-entities.md`, `02-span.md` §3, §4.3 |
| Q2 | **[CONFIRM]** | **Tool calls / retrieval documents / per-document scores are not first-class.** Parallel tool calls in one generation have no typed correlation to their `tool_call` spans (only a `call.id` string inside opaque `output`); retrieval document lists and their per-doc relevance scores cannot be `Score` entities (scores attach to span/trace/session subjects, not sub-rows). Deliberate per LM-2/LM-8. | OTel GenAI, OpenInference | `02-span.md` §1, `04-score.md` §5 |
| Q3 | **[CONFIRM]** | **`EVALUATOR`/`RERANKER` collapse to `span`**, and an evaluator's output is conceptually a Score, but the mapping does not route it to one. `raw_kind` preserves the string but "is never interpreted by the Query API," so eval spans are query-indistinguishable from generic spans. Deliberate per LM-1. | OpenInference, Langfuse | `02-span.md` §2.2 |
| Q4 | **[CONFIRM]** | **`status` tri-state loses Langfuse `DEBUG`/`WARNING`.** Non-error severities collapse to `unset`; a real WARNING (e.g. provider throttled) survives only as `llmobs.raw.level`, not queryable. Deliberate (OTel-canonical status). | Langfuse | `02-span.md` §4.1 |
| Q5 | **[CONFIRM]** | **`tags` are trace-only**, but OpenInference/OTel attach tags to any span; span-level tags land as flattened `attributes` keys and do not union into the trace `tags` set. | OpenInference | `03-trace.md` §2 |
| Q6 | **[CONFIRM]** | **`prompt_ref` is not promoted**, so Langfuse's first-class "filter/group generations by prompt name+version" becomes a soft, possibly-dangling reference rather than a queryable dimension. Deliberate per LM-12 (prompt is a plugin). | Langfuse | `02-span.md` §5.4 |
| Q7 | **[CONFIRM]** | **Provided cost wins → un-re-priceable.** A client-supplied `cost_details` (common from Langfuse SDKs) short-circuits kernel derivation and leaves `pricing_snapshot_ref` null, so re-pricing (`06` §5) has nothing to target; client cost errors become permanently authoritative. `cost_source` records provenance (an improvement over Langfuse), but the precedence stands. Deliberate per LM-4. | Langfuse, OpenLLMetry | `06-usage-cost.md` §4 |

Coverage gaps (honest nulls — a canonical field with no source in a dialect, not
a lossy landing): `completion_start_time` (TTFT is a metric, not a span attribute
in OTel; absent in OpenInference/OpenLLMetry), `version` (no per-call version in
OTel/OpenInference/OpenLLMetry), `prompt_ref` (only Langfuse has prompt linkage),
and the usage keys `cache_write`/`reasoning`/`audio`/`image` (unfillable from
plain OTel GenAI).

See the design-session report for the recommended dispositions and the questions
requiring a decision before the model is promoted past `v1alpha1`.
