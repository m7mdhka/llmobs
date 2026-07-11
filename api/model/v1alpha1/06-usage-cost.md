# Usage and Cost (`v1alpha1`)

**Section type:** Normative except where marked *Informative*.

Usage and cost apply to `generation_shaped` spans (`02-span.md` §5). The model
stores **both** what the client provided **and** what the kernel resolved, keeps
them as open-keyed maps, and records enough provenance to re-derive cost later.

## 1. Fields (Normative)

| Field | Type | Semantics |
|---|---|---|
| `provided_usage_details` | map<string, integer≥0> | Usage counts exactly as sent by the client. |
| `usage_details` | map<string, integer≥0> | Resolved usage (provided, or kernel-derived). §3. |
| `provided_cost_details` | map<string, decimal> | Cost exactly as sent by the client. |
| `cost_details` | map<string, decimal> | Resolved cost (provided, or kernel-derived). §4. |
| `total_cost` | decimal \| null | Scalar total (§4.1). |
| `cost_source` | `provided` \| `derived` \| null | How `cost_details` was obtained (§4). |
| `pricing_snapshot_ref` | reference \| null | The price entry used for derivation (§5). |

All four maps default to `{}`. Usage values MUST be non-negative integers; cost
values are decimals (a fixed-scale decimal, not a binary float, to avoid drift on
money — adapters MUST preserve at least 12 fractional digits).

> Evidence: Langfuse stores paired `provided_usage_details`/`usage_details`
> (`Map(..., UInt64)`) and `provided_cost_details`/`cost_details`
> (`Map(..., Decimal64(12))`) plus a scalar `total_cost`, so it retains both the
> client's numbers and its resolved numbers (study Ch. 05 §1; Ch. 06 §5.1). This
> model keeps the dual maps and the `Decimal64(12)`-equivalent precision, and adds
> `cost_source` + `pricing_snapshot_ref` (§4, §5) for re-derivability.

## 2. Provided maps are the only wire-writable surface (Normative)

A normalizer MUST write usage/cost the client supplied **only** into the
`provided_*` maps. It MUST NOT populate `usage_details`, `cost_details`,
`total_cost`, `cost_source`, or `pricing_snapshot_ref` — those are produced by the
kernel enrichment step (§3, §4), never by a wire format.

## 3. Well-known usage keys and resolution (Normative)

### 3.1 Well-known keys

The usage/cost maps are open, but these keys are **well-known** and normalizers
SHOULD map onto them so that cross-provider aggregation works:

| Key | Meaning |
|---|---|
| `input` | Input/prompt tokens (or the unit's input count). |
| `output` | Output/completion tokens. |
| `total` | Total tokens; if absent, consumers compute it as the sum of the other keys. |
| `cache_read` | Tokens served from a prompt cache (read). |
| `cache_write` | Tokens written to a prompt cache. |
| `reasoning` | Reasoning/thinking tokens (models that bill these separately). |
| `audio` | Audio tokens/units. |
| `image` | Image tokens/units. |

Additional keys are permitted (the map is open); adding a well-known key is
additive. A normalizer MUST map a provider's name onto a well-known key only where
the rename is a pure alias (e.g. OTel `gen_ai.usage.input_tokens` → `input`,
`output_tokens` → `output`).

**Provider-reported semantics, as-is (Normative, F3).** Usage values are recorded
with the **provider's own semantics** — `input` is whatever that provider calls
input, and cache buckets (`cache_read`/`cache_write`) are **additive detail keys**.
A normalizer MUST NOT reinterpret across providers: it MUST NOT subtract cached
tokens from `input`, MUST NOT synthesize provider-specific fictions, and MUST NOT
assume `input` is net-of-cache or inclusive-of-cache — it stores what the provider
reported. **Cache accounting is not comparable across providers**, and consumers
MUST NOT assume it is. Recording honest provider semantics is preferred over
silently normalizing to a cross-provider fiction.

> Evidence: Langfuse normalizes token names but also **subtracts** cached tokens from
> the input total to avoid double counting (study Ch. 05 §5, Ch. 06 §5.1) — a
> cross-provider reinterpretation that bakes in an assumption about whether `input`
> includes cache. F3 rejects that: the model keeps `cache_read`/`cache_write` as
> first-class additive keys and records provider values verbatim, leaving
> interpretation to the (provider-aware) consumer rather than a lossy normalize step.

### 3.2 Resolution precedence for `usage_details` (Normative)

The kernel enrichment stage computes `usage_details` as follows:

1. If `provided_usage_details` is non-empty, `usage_details` = a normalized copy
   of it (drop negative/non-integer values; synthesize `total` as the sum of the
   other keys only if no `total` key is present).
2. Otherwise, the kernel MAY derive usage (e.g. tokenization) **only** when a
   model is resolved and the span's `status.code` is not `error`. Derived usage
   populates at least `input`, `output`, `total`.
3. Otherwise `usage_details` is `{}`.

> Evidence: Langfuse tokenizes to fill usage **only if** a model matched, no usage
> was provided, and `level != ERROR`; provided usage is otherwise normalized and a
> missing `total` is synthesized as the bucket sum (study Ch. 06 §5.2). This model
> adopts the same precedence (provided usage > derived usage; skip derivation on
> error).

## 4. Cost resolution (Normative) — LM-4

The kernel enrichment stage computes `cost_details`, `total_cost`, and
`cost_source`:

1. **Provided cost wins and short-circuits derivation.** If
   `provided_cost_details` is non-empty, then `cost_details` = a copy of it,
   `cost_source = "provided"`, and the kernel MUST NOT derive any additional cost
   point. `pricing_snapshot_ref` is null.
2. Otherwise, if a price entry resolves, the kernel derives `cost_details` by
   multiplying each `usage_details[k]` by the matching unit price, sets
   `cost_source = "derived"`, and sets `pricing_snapshot_ref` to the price entry
   used (§5). Price lookup keys on the promoted `provider` and `model` fields
   (`02-span.md` §5, F7) — the served model, not the requested one — so the
   derivation inputs are typed fields rather than attribute-map lookups.
3. Otherwise `cost_details = {}`, `total_cost = null`, `cost_source = null`.

### 4.1 `total_cost` (Normative)

`total_cost` is the scalar total. When `cost_details` has a `total` key,
`total_cost` equals it; otherwise it is the sum of the `cost_details` values.
`total_cost` is `null` when `cost_details` is empty.

> Evidence: Langfuse's provided cost short-circuits all derivation (a single provided
> cost key disables price-table lookup), otherwise it multiplies `usage_details[k]` by
> the tier price for `usageType == k` and sums a missing `total`; `total_cost` is the
> denormalized scalar (study Ch. 06 §5.2 step 4, §6). LM-4 keeps provided-wins
> precedence but records `cost_source` so a consumer can tell provided from derived —
> which Langfuse cannot distinguish after the fact.

## 5. Pricing snapshot and re-derivability (Normative) — LM-4

Every **derived** cost MUST record a `pricing_snapshot_ref`: a reference
(`07-references.md`) identifying the exact price entry (its id and version) used
for the derivation. This makes historical cost **re-derivable**: a later price
correction does not silently invalidate stored costs, and a re-pricing run can
recompute `cost_details` for the affected spans.

Re-pricing MUST be performed as a **backfill job on the kernel `jobs` primitive**
(not an inline mutation): a job reads spans whose `pricing_snapshot_ref` matches
the corrected price entry and re-emits `upsert` events recomputing `cost_details`,
`total_cost`, and `pricing_snapshot_ref`. This composes with the normal merge fold
(`05-update-semantics.md`).

> Evidence: In Langfuse cost is resolved at ingest against a point-in-time price
> table and stored denormalized, so a price change or mis-match requires reprocessing
> history — which it does via dedicated background migrations
> (`addGenerationsCostBackfill`) — but it stores **no** reference to which price row
> produced a given cost, making the backfill a blunt full-rewrite (study Ch. 06 §5.2,
> §6; digest §4). LM-4 diverges by storing `pricing_snapshot_ref` so re-pricing is
> targeted and history is precisely re-derivable, and by mandating the `jobs`
> primitive rather than a bespoke migration.

## 6. Read-time reduction (Informative)

Consumers that need scalar `input`/`output`/`total` usage or cost from the open
maps compute them by summing keys by well-known prefix (all `input*` → input,
etc.) and reading `total` directly. This is a presentation reduction; the
authoritative values are the maps and `total_cost`.

> Evidence: Langfuse's read layer reduces the open maps to `input/output/total`
> scalars by summing keys `startsWith("input")` / `startsWith("output")` and reading
> `total` (study Ch. 06 §5.4). Recorded here as informative guidance for Query API
> implementers; it is not a storage rule.

## 7. Derivation stage rules (Normative) — ADR-0025 / K2

The kernel enrichment stage that derives `usage_details`/`cost_details` (§3.2, §4) is
**not built yet** (`pipeline/stages.go` is a no-op). These rules are pinned before it
exists so it is built correct — each is a failure mode the Langfuse mine observed
shipped. When the stage is built it MUST honor them:

- **7.1 Aggregate spans MUST NOT double-count leaf usage.** An `agent_step` /
  `invoke_agent` span frequently carries the *same* usage as its child model-call
  span. Trace-level cost aggregation (a `SUM(total_cost)` over a trace's spans) MUST
  NOT count both — that doubles the trace's cost. Derivation MUST treat an aggregate
  span's usage as suspect: either it is excluded from trace-level cost roll-ups, or it
  is flagged, but it is never summed alongside a child that carries the same usage.
  (Evidence: Langfuse #14808 zeroes usage on AI-SDK agent spans.)
- **7.2 Never sum `input + cache` as disjoint.** F3 (§3.1) forbids assuming whether
  `input` is inclusive- or net-of-cache. Therefore the synthesized `total` (§3.2.1)
  and any derived cost MUST be computed from `input + output` only; `cache_read`,
  `cache_write`, and `reasoning` are additive **detail** keys and MUST NOT be summed
  into `total` or multiplied-and-added as separate cost lines that also inflate the
  input line. Summing input + cache is the mirror image of Langfuse's
  subtract-cache-from-input bug (#14902/#14945) — F3 protects ingest, this rule
  protects derivation.
- **7.3 `model` without usage MUST NOT fabricate cost.** §3.2.2 permits usage
  derivation only when a model resolves and `status.code != error` — but a
  wrapper/agent span that has a `model` set and **no** provided usage MUST NOT be
  tokenized into estimated usage and then priced. Gate estimation on the span being a
  leaf generation whose usage is genuinely absent (not merely carried by a child),
  or the trace accrues phantom cost. (Evidence: Langfuse #14945 — a model set without
  usage flipped a span into estimated-usage mode.)
