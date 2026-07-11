# Issue #13 — cost derivation + pricing snapshot (enrich stage) — design notes

Banked design input for the **unbuilt** enrich/cost-derivation stage
(`pipeline/stages.go` is a no-op today). Contract rules live in
`api/model/v1alpha1/06-usage-cost.md` §7 (7.1–7.6 pinned); this file is the
research provenance + the fixture/conformance obligations behind them.

Primary source this round: the **Opik (Comet)** cross-over mine, Round 1 — a
second incumbent, architecturally unlike Langfuse (Java + ClickHouse + MySQL).
Full report: `tmp/opik-crossover-round-01.md` /
`docs/research/opik-issues/round-01-findings.md`. Rules confirmed by BOTH
incumbents are flagged — that is the strongest "make immunity demonstrable"
signal we have.

## 1. Pinned rules (see 06-usage-cost.md §7)

| Rule | One-line | Evidence |
|---|---|---|
| 7.1 | Aggregate spans MUST NOT double-count leaf usage | **Langfuse #14808 AND Opik #4695** (dual-incumbent) |
| 7.2 | Never sum `input + cache` as disjoint; `total` = input+output only | Langfuse #14902/#14945 |
| 7.3 | `model` without usage MUST NOT fabricate cost | Langfuse #14945; **Opik #4508** (DSPy cache hit: 0 tokens, non-zero cost) |
| 7.4 | Cache detail keys priced at their OWN rate; cache pricing is DATA-DRIVEN off the price entry, never a per-provider allow-list | **Opik #5618** (LiteLLM cache at full input rate, 5–10× over-report); **Opik #6976** (Google falls through to flat cost because calculator registered for anthropic/openai/bedrock only) |
| 7.5 | Tiered/threshold pricing applied; snapshot captures the tier schedule version | **Opik #6982** (`*_above_200k_tokens` never parsed → long-context under-report) |
| 7.6 | Model-key normalization symmetric at load AND lookup; single provider-canonicalization table | **Opik #5621** (prefix stripped at load not lookup → all LiteLLM OTel spans cost=0); **Opik #6928** (Vertex AI routes to `provider='gemini'`) |

**Worked example for F3 (§3.1).** Langfuse *over-subtracts* cache from input
(#14902); Opik *under-discounts* it — bills cache at full input rate (#5618) or
skips cache pricing entirely for un-allow-listed providers (#6976). Two
independently-architected systems get cache cost wrong in **opposite
directions**. This is the concrete argument for why ingest stays verbatim (F3)
and only derivation applies rates: the surface is provably hard.

## 2. Conformance obligations (wire into the enrich stage's FIRST commit)

- **[Priority — dual-incumbent] #4695 aggregate-usage fixture.** LangGraph +
  Pydantic-AI OTel trace: a parent/agent span + one leaf generation span
  carrying usage. Assert trace-level usage == leaf usage, **not 2×** (§7.1).
  Standing conformance vector; this is the one Langfuse #14808 and Opik #4695
  both prove is real.
- **#4508 no-cost-without-usage vector.** Model set + usage absent (cache hit)
  ⇒ `cost_source=null`, `total_cost` empty (§7.3).
- **#5618/#6976 cache-rate vector.** A price entry carrying a cache-read rate +
  `cached_tokens` present ⇒ cache tokens priced at the cache rate, not the input
  rate; and a provider with NO cache rate on its entry is simply not cache-priced
  — with no code branch keyed on provider name (§7.4).
- **#6982 tiered-pricing vector.** `gemini-2.5-pro` call > 200k tokens against a
  tiered price entry ⇒ above-threshold tokens billed at the tier rate; snapshot
  ref records the tier schedule version (§7.5).
- **#5621/#6928 model-key + provider-canon fixtures.** Spans with
  `gen_ai.request.model` ∈ {`openai/gpt-4o`, `openrouter/openai/gpt-3.5-turbo`,
  `anthropic/claude-3-5-sonnet-20241022`}; provider aliases {`vertex_ai`,
  `gemini`, `google_ai`; `azure`, `openai`}. Assert symmetric load/lookup
  normalization and single-table provider canonicalization; raw preserved (§7.6).

## 3. Immunity confirmations (no action — cite as design wins)

- **Opik #5619** — `getCostFromMetadata()` reads a `total_tokens` count (150,000)
  and returns it as $150,000. We are immune by construction: the dual maps keep
  `provided_cost_details` (typed decimals ≥12 digits, §1) strictly separate from
  usage counts. A token count can never be read as a cost.

## 3a. Round-2 additions (Opik mine)

- **§7.4 generalized (Opik #7137).** Audio tokens carry `input_cost_per_audio_token`
  (up to 16× the text rate) and were billed at the text rate because the field was
  never read. This proved cache is not special — the rule is now "**every** detail key
  (cache/audio/reasoning/future) priced at its own rate off the price entry; base rate
  bills only the residual `input − Σ(specially-priced buckets)`." Fixture: an audio
  span bills `audio_tokens` at the audio rate and the remainder at input.
  - **Meta-lesson (both incumbents).** Every incumbent cost bug in this class —
    Langfuse and Opik alike — comes from **enumerating special cases** (a provider
    allow-list, a bucket the mapper forgot) instead of **driving uniformly from the
    price entry**. The un-enumerated case silently falls through to the flat rate.
    Design mandate: derivation prices detail keys by iterating the price entry's rate
    fields, never a hand-maintained case list.
- **Null usage detail objects (Opik #3397).** `usage` with
  `prompt_tokens_details=None` / `completion_tokens_details=None` crashed Opik's usage
  builder (`ValueError`). Our normalizer must treat a null detail object as **absent
  buckets**, never error (§9.1 — bad data never fails a valid path). Fixture: usage
  with null detail objects normalizes cleanly.
- **429 sub-classification (Opik #7148) — banked in ADR-0027 D6, cross-ref here.** For
  any outbound-LLM retry in a future eval / online-scoring plugin AND for the WAL D6
  classifier: `429 insufficient_quota` = permanent (fail-fast), `429` rate-limit =
  transient (retry). Fixture: quota-429 → no retry; ratelimit-429 → retry.

## 4. Related merge-semantics obligation (adjacent, not cost)

- **Opik #6761** — `opik_args` span merge rebuilds params copying only a subset
  of fields, silently dropping `environment`/`thread_id`. Pin a vector proving
  our per-field-group fold (V17, `05-update-semantics.md` §3) cannot drop an
  unspecified sibling field when a partial-object update lands. Likely already
  immune (fold is per field-group, not whole-entity); prove it.
