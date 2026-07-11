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

Cost is `float64` in Go/JSON and `Float64` on the ClickHouse (scale) adapter (with
`cost_details` a JSON string on both). Per-span derived cost is computed once by the
shared derivation and stored identically on both adapters — it is byte-identical because
the derivation is deterministic (a stable-order sum) and both engines store the same
`float64`. Trace-level `total_cost` (a query-time SUM over leaf spans) is rounded to a
shared decimal scale so the roll-up is byte-identical cross-adapter even though Postgres
sums exact `NUMERIC` while ClickHouse accumulates `Float64`.

## Aggregate spans don't double-count (R5)

An agent/tool aggregate span (`invoke_agent`, `execute_tool`, …) that carries the sum of
its child model calls' usage does **not** have that usage extracted — otherwise the
trace's cost would double. Cost accrues on the leaf model-call spans only.

## Re-pricing history after a price or discount change (M4)

Because every derived cost records the exact price version it used
(`pricing_snapshot_ref.id`), a price correction is **deterministic to re-apply** — not a
best-effort full rewrite. When you edit a price (which appends a new version) or change a
project discount, trigger a re-pricing backfill:

```
POST /v1alpha1/pricing/reprice
# Re-price spans priced against a superseded version (global — a price is instance-wide):
{ "scope": "price", "provider": "openai", "model": "gpt-4o", "version": 1 }
# Re-price your project's derived spans after a discount change (tenant-scoped):
{ "scope": "discount" }
```

The endpoint is admin-gated exactly like a price edit (it mutates money across history)
and returns `202` with a `run_key`. The job runs in the background:

- **Resumable & bounded.** It scans in bounded chunks on a `(ts, project_id, id)` cursor
  and runs on its own generous budget (never the interactive read timeout), so a large
  history re-prices over time and survives a restart.
- **Never drops a cost on a blip.** A transient price-store/persist failure stops the run
  loud and resumable — it is *never* converted into a null (which would drop the existing
  cost). It resumes and finishes once the backend recovers.
- **Idempotent.** A span whose re-derived cost equals its current cost is left untouched,
  so re-running the same re-price is a no-op. Re-priced spans get a fresh
  `pricing_snapshot_ref` pointing at the new version, so the chain stays re-derivable.
- **Provided cost is never touched.** A span whose cost was provided by the SDK (`R1`)
  carries no snapshot ref and is never scanned — provided always wins.
- **Tenant-scoped.** A discount re-price touches only that project's spans; it cannot
  cross a project boundary. (A global price re-price applies to every project, because a
  global price is instance-wide.)
- **Both profiles, both engines.** In a scale deployment the run covers spans in both the
  lite (Postgres) and scale (ClickHouse) tiers; the re-derived cost is identical on both.
