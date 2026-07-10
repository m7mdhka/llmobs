# Positioning (internal)

Honest, cited, no FUD. Every claim points to a source — a file in this repo, a
public Langfuse discussion (mined 2026-07-10, see
[`research/langfuse-discussions/`](research/langfuse-discussions/)), or a marked
external analysis. This document feeds the README and the launch post; keep it
defensible. Where a claim's source is not yet committed here, it is flagged so it
is not shipped uncited.

The four pillars are the durable reasons LLMObs exists that an incumbent cannot
simply copy — not feature checklists (features get copied), but structural
positions rooted in our architecture and in the incumbent's constraints.

---

## Pillar 1 — Storage neutrality

**Claim.** LLMObs is storage-adapter-neutral by construction: the dataplane
depends only on a storage interface, not on any one engine, and every adapter is
held to one conformance suite. A deployment can run Postgres (lite) or ClickHouse
(scale) — or contribute Timescale, or another engine — behind the same Query API
with identical observable semantics.

**Evidence (ours).**
- The telemetry boundary is a Go interface (`kernel/internal/storage`), and
  `query`/`pipeline` depend only on it — grep-proven that no dataplane package
  imports the concrete adapter (PR-E1).
- One conformance harness (`kernel/tools/conformance`) replays the normative
  merge vectors + order-independence against *any* adapter; the adapter-author
  guide is [`adapters/authoring-a-storage-adapter.md`](adapters/authoring-a-storage-adapter.md).
- The DSL is compiled *per adapter* behind the contract (ADR-0016 canonical
  model; ADR-0019 query DSL), so a new engine is an adapter change, never a
  contract change.

**Why the incumbent structurally can't match it.** **ClickHouse acquired
Langfuse — announced January 16, 2026**, alongside a $400M Series D led by
Dragoneer at a ~$15B valuation; both companies committed publicly to keeping
Langfuse MIT-licensed with self-hosting as a first-class path. Primary sources:
[ClickHouse blog](https://clickhouse.com/blog/clickhouse-acquires-langfuse-open-source-llm-observability),
[Langfuse blog](https://langfuse.com/blog/joining-clickhouse),
[funding context](https://clickhouse.com/blog/clickhouse-raises-400-million-series-d-acquires-langfuse-launches-postgres).
ClickHouse did not acquire Langfuse to move it onto a new engine — it acquired
the application layer sitting atop the database it already powered. Langfuse's v4
architecture independently doubles down on ClickHouse as *the* store — a single
wide, mostly-immutable ClickHouse table is the centerpiece of the redesign
([#12518, deep-read](research/langfuse-discussions/deep-reads/12518-v4-architecture.md)).

The conclusion is **structural, not speculative**: a product whose owner sells
ClickHouse has no incentive — and arguably a disincentive — to become
storage-neutral, because neutrality would let customers run the platform
*without* the owner's database. This is not a rug-pull prediction (ClickHouse's
business model is genuinely OSS-aligned and a relicense is not being claimed
here); it is the plain observation that storage neutrality is a position their
cap table makes expensive and ours makes free.

---

## Pillar 2 — We never strand self-hosters on a migration cliff

**Claim.** Because storage is an adapter behind a versioned contract, evolving the
physical schema is an *adapter* change, not a product-wide migration users must
survive. The Query API and canonical model stay stable while the storage layout
changes underneath.

**Evidence (theirs — the anti-pattern).** Langfuse's v4 is a full data-model
migration (a wide, mostly-immutable table replacing read-time dedup), and the
self-hosted migration path lagged the product badly:
- Langfuse **Cloud shipped v4 in March 2026**, but **existing self-hosted
  deployments still had no migration path as of late June 2026** — with multiple
  members asking for an ETA (savitha-suresh, timur-3c, ashgold, arunkumar-maker),
  and a maintainer conceding it is *"hard to guarantee release dates… without a
  well documented migration path"*
  ([#12518, deep-read](research/langfuse-discussions/deep-reads/12518-v4-architecture.md)).
- Older SDKs cause up to a 10-minute data delay in the new UI during the
  transition (same source).

**Evidence (ours).** Two profiles, one Query API; a storage-adapter contract with
a conformance harness (PR-E1); per-field provenance + merge-on-write so the fold
is deterministic and re-delivery idempotent (issue #17, PR-B2). A layout change
like "insert-only final rows" is exactly the kind of thing that lands as a new
adapter passing the same vectors — invisible to the Query API and to the operator.

**Positioning.** Make *"we never strand self-hosters on a migration cliff"* an
explicit, load-bearing promise. It is the sharpest contrast the mined data offers:
the loudest, most-upvoted self-hosting pain in Langfuse's own discussions is
exactly the failure mode our architecture is built to avoid.

---

## Pillar 3 — Vendor-neutral, community governance

**Claim.** LLMObs is Apache-2.0 core, governed in the open, with no feature-gating
of the core and no single-vendor control of the contract.

**Evidence (ours).** [`GOVERNANCE.md`](../GOVERNANCE.md): the core is Apache-2.0
and "never feature-gated"; maintainership is by consensus on a sustained track
record; changes require DCO sign-off and code-owner review; disputes resolve by
maintainer consensus (majority fallback). First-party plugins use only the public
plugin API (the dogfood rule), so maintainers experience the platform the way
plugin authors do — governance and architecture reinforce each other.

**Contrast (fair, not FUD).** An incumbent owned by a database vendor answers, in
the end, to that vendor's commercial roadmap. That is a legitimate way to build a
company; it is simply a *different* alignment than a vendor-neutral, plugin-first
project whose contract no single vendor owns. We compete on that alignment, not on
disparagement.

---

## Pillar 4 — Kernel-plus-plugins vs. monolith economics

**Claim.** A small kernel plus a real plugin ecosystem beats a monolith on the
economics of demand: the highest-demand asks are integrations the core team will
never prioritize, and a monolith can only ship what its roadmap funds.

**Evidence (the flagship proof).** The #1 community request by a wide margin is
**n8n tracing — 381 votes**, 3.5× the next item — and Langfuse *structurally
cannot ship it*: maintainer @marcklingen states it requires n8n-core
instrumentation that does not exist
([#4397, deep-read](research/langfuse-discussions/deep-reads/4397-n8n.md)). In our
model it is a **compat plugin** (ingest capability, own endpoint) that polls n8n's
execution API and translates workflow runs into traces server-side — a
container + manifest, no upstream instrumentation required, shippable by anyone
without touching kernel code. The single strongest validation of the plugin
thesis in the entire corpus.

**Evidence (the economics).** The integrations cluster dominates *discussions* in
a way it did not dominate *issues* (see
[`research/langfuse-discussions/clusters.md`](research/langfuse-discussions/clusters.md));
demand is a long tail of target systems (n8n, LangChain, LlamaIndex, LiteLLM,
Dify, Flowise, …), each of which is a plugin/normalizer in our model but a
roadmap line item in a monolith. A monolith serves the head of that distribution
and drops the tail; a plugin ecosystem serves the tail by making the marginal
integration someone else's afternoon, not the core team's quarter.

**Evidence (the "Not Planned" figure).** Of Langfuse's **25 most-upvoted closed
issues, 10 (40%) are closed `not_planned`** — including conditional prompt
rendering, dataset-item comments, auto-render images, `fetch_score` in the SDKs,
and per-cloud pricing rows. The ranked dataset and query are committed at
[`research/langfuse-issues/`](research/langfuse-issues/) (mined 2026-07-10,
reproducible from the recorded Search API query); the analysis is in
[`research/langfuse-issues/findings.md`](research/langfuse-issues/findings.md).
This is monolith economics: a single roadmap cannot fund a long tail of niche
asks, so most lose — whereas in a kernel-plus-plugins model each is a community
plugin that never queues behind a core team. The figure is exact for that defined
set (top-25 closed by votes), not a claim about all issues.

---

## What this is not

Not a claim that LLMObs is more mature than Langfuse today — it is younger and has
less built. These pillars are about *structural* advantages that compound as both
projects grow, not a feature-parity boast. The launch narrative should lead with
"the votes ratify decisions we already made" (two of the community's top-ten asks
— the `embedding` kind and session/categorical scores — shipped before they were
requested; see `clusters.md`) and with the four structural positions above, not
with a feature-count comparison we would lose.
