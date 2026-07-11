# Deferred fixtures — banked span shapes for dialects we do not normalize yet

These are **recorded span shapes for future normalizers**, harvested from the Langfuse
merged-PR mine (Arc K / ADR-0025). They are **not wired to any test** — there is no
normalizer for these dialects today (`normalize/` implements only raw OTel GenAI;
OpenInference/OpenLLMetry/Flue are unbuilt). When one of those dialects is built, move
its fixture up into a live `<dialect>/` directory, add the `.expected.json` golden,
and register it in `fixture_test.go` (which globs `*.otlp.json` pairs).

Synthetic only (no real payloads), same as every fixture (`../README.md`).

## `openinference/cost-usage.otlp.json` — OpenInference cost + usage (#14472)

A distinct dialect (OpenInference), NOT raw OTel GenAI. What a future OpenInference
normalizer must handle:

- Usage lives under `llm.token_count.*`: `llm.token_count.prompt` → `input`,
  `llm.token_count.completion` → `output`, `llm.token_count.total` → `total`.
- Run-level total cost is `llm.cost.total` → `provided_cost_details.total` (emitted on
  the **parent AGENT span** by the Claude Agent SDK OpenInference instrumentor).
- **Precedence fork to assert:** when BOTH `gen_ai.usage.cost` and `llm.cost.total`
  are present, `gen_ai.usage.cost` wins (Langfuse's own precedence — untested in their
  suite, so a good adversarial case for us). The second span in the fixture carries
  both.
- I/O is `input.value` / `output.value` (JSON strings), model is `llm.model_name`.

## `flue/span-tree.otlp.json` — Flue framework span tree (#14332)

A distinct dialect (`@flue/opentelemetry` scope), NOT raw OTel GenAI. Five span types,
detected by attribute presence, each routing content into dedicated input/output
(never the raw attribute bag):

| Flue span | canonical kind | detect via | input / output keys |
|---|---|---|---|
| `flue.workflow` | span | `flue.workflow.name` | `flue.workflow.payload` / `flue.workflow.result` |
| `flue.operation` | span | `flue.operation.id`/`.kind` | (none) / `flue.operation.result` |
| model turn (`chat {model}`) | generation | `gen_ai.operation.name`/`gen_ai.request.model` | `flue.turn.input` / `flue.turn.output` |
| `flue.tool` | tool_call | `flue.tool.name`/`.call_id` | `flue.tool.arguments` / `flue.tool.result` |
| `flue.task` (sub-agent) | agent_step | `flue.task.id`/`.agent` | `flue.task.prompt` / `flue.task.result` |

Note: the model-turn span rides raw `gen_ai.*` (a raw-GenAI normalizer would map it as
a generic generation), but the tool/task/workflow/operation spans are pure `flue.*`
and fall through as generic spans — they need their own normalizer. All `flue.*` keys
should be consumed from the raw bag once mapped. Type detection is by attribute
presence, not scope name (a Langfuse reviewer flagged that as fragile — prefer gating
on the `@flue/opentelemetry` scope when we build it).

## OpenAI Responses tool-call output shapes (#14654 / #14671) — for a future tool-call parser

Not a normalizer fixture (we store I/O opaque, so we're immune to mis-parsing today).
Banked here as reference for whenever a tool-call *parser* is built (kernel or UI). The
output carries function-call items in two forms that BOTH must be handled, keyed by
`call_id` in preference to `id`:

- **Standalone object:** `{ "id":"fc_…", "type":"function_call", "status":"…",
  "arguments":"{…}", "call_id":"call_…", "name":"…" }`
- **Array of the same:** `[ {…function_call…}, … ]` (observation `OpenAI.responses`).
  It must NOT be mis-detected as a LangChain/LangGraph message array — those items lack
  `role`/`additional_kwargs`.

Each normalizes to ChatML `{ role:"assistant", tool_calls:[{ id: call_id, name,
arguments, type:"function" }] }` — `call_id` (stable cross-request) preferred over the
item `id`.

## SDK attribution (#14593) — an ingest-API header note, not a fixture

SDK provenance is NOT a span-attribute concern. Langfuse identifies the SDK from HTTP
request headers `x-langfuse-sdk-name` / `x-langfuse-sdk-version` and the public key
from the auth scope — captured at the ingestion API, not the normalizer. If we ever
want SDK provenance on canonical records, capture it at the ingest-API/auth seam from
headers + auth context; a normalizer never sees it. (No fixture.)
