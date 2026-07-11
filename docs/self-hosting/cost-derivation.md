# Cost derivation — how `total_cost` is computed

LLMObs derives cost **once at ingest** (the enrich stage, after normalize, before
persist), stores it, and never recomputes it at read time. This is deliberate: cost is
resolved against the price table as it stood when the span arrived, and a later price
change re-prices history via a targeted backfill (M4), not a read-time re-resolution.
The rules are pinned in [`api/model/v1alpha1/06-usage-cost.md`](../../api/model/v1alpha1/06-usage-cost.md)
§4–§7 and are conformance-tested against the exact bugs both incumbents (Langfuse, Opik)
shipped.

## What happens to a span

1. **Provided cost wins (R1).** If your instrumentation sent cost, it is stored verbatim,
   tagged `cost_source=provided`, and **derivation is skipped entirely** (no price lookup,
   `pricing_snapshot_ref` null). Your numbers are never overwritten.
2. **Otherwise the kernel derives** from resolved usage and the price table — if a model
   resolves and a price entry exists. `cost_source=derived`, and `pricing_snapshot_ref`
   records the exact price entry version used (so cost is re-derivable).
3. **No usage → no cost (R4).** A span with a `model` set but **no usage** is never
   tokenized into estimated usage and priced — `total_cost` stays null. Cost is never
   fabricated.
4. **No price entry → null, never zero.** An unknown model leaves cost null; add a price
   entry (below) and re-price with the backfill.

## Pricing is data-driven, never a code path (R2)

Cost keys off the **presence of a rate** on the price entry, one path for every provider.
Adding a provider or a new token bucket is [editing the price table](scaling-lite-to-scale.md),
never a code change. Concretely:

- **Each detail bucket bills at its own rate on the residual (R3/§7.7.1).** `cache_read`,
  `audio_input`, `reasoning`, … are billed at their own rate, and the base (`input`/
  `output`) bills only its **residual** = base − Σ(buckets that reduce it). So cached
  tokens are discounted, not charged at the full input rate, and never double-charged.
- **Tiers are graduated and apply to the residual (R7/§7.7).** Above a threshold, only the
  tokens above it bill at the tier rate (tax-bracket), computed on the residual — never a
  cliff, never on the raw count.
- **Per-project discount.** A project's negotiated discount multiplies its derived cost.

## Model & provider names normalize symmetrically (R6)

`openai/gpt-4o`, `openrouter/openai/gpt-4o`, and `gpt-4o` resolve to the same price entry;
the Vertex AI / Gemini / Google-AI family collapses to one canonical provider. The exact
same normalization runs when a price is written and when it is looked up, so a
provider-prefixed model never silently records zero cost.

## Precision

Cost is a fixed-scale decimal: `float64` in Go/JSON, `Decimal64(12)` on the ClickHouse
(scale) adapter. Derived cost is computed once and stored identically on both the
Postgres and ClickHouse adapters; the documented cross-adapter bound is equality to 12
fractional digits.

## Aggregate spans don't double-count (R5)

An agent/tool aggregate span (`invoke_agent`, `execute_tool`, …) that carries the sum of
its child model calls' usage does **not** have that usage extracted — otherwise the
trace's cost would double. Cost accrues on the leaf model-call spans only.
