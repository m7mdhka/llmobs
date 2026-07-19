# Positioning (internal)

Honest, cited, no FUD. Every claim points to a source — a file in this repo, or a
**public** competitor artifact (a Langfuse discussion/issue, an Opik issue) linked
directly to its primary URL so any reader can verify it without trusting our
summary. The competitive figures are reproducible: the [Evidence
appendix](#appendix--evidence-base) records the exact queries, the mining dates,
and the ranked tables the claims are computed from. This document feeds the README
and the launch post; keep it defensible. Where a claim's source is not verifiable
from a primary URL, it is flagged so it is not shipped uncited.

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
([discussion #12518](https://github.com/orgs/langfuse/discussions/12518); quoted in
the [Evidence appendix](#c-langfuse-v4-the-migration-cliff-discussion-12518)).

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
  ([discussion #12518](https://github.com/orgs/langfuse/discussions/12518) — quotes
  and comment vote-counts in the
  [Evidence appendix](#c-langfuse-v4-the-migration-cliff-discussion-12518)).
- Older SDKs cause up to a 10-minute data delay in the new UI during the
  transition (same source); real-time requires Python SDK v4 / JS-TS SDK v5.
- The v4 self-host preview announced 2026-06-11 covers **new deployments only**
  (discussion #14157) — the migration path for *existing* self-hosters is the gap.

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
**n8n tracing — 381 votes**
([discussion #4397](https://github.com/orgs/langfuse/discussions/4397)), 1.6× the
next target system and larger than several whole demand clusters — and Langfuse *structurally
cannot ship it*. Maintainer @marcklingen, verbatim: *"As n8n is a platform product,
we cannot build an external integration as this needs to be added to the n8n project
itself… this very much depends on open tracing/instrumentation capabilities in n8n
which do not exist."* In our model it is a **compat plugin** (ingest capability, own
endpoint) that polls n8n's execution REST API (`/executions/{id}` exposes node
inputs, outputs, and sub-workflow structure) and translates workflow runs into
canonical spans server-side — a container + manifest, no upstream instrumentation
required, shippable by anyone without touching kernel code. The single strongest
validation of the plugin thesis in the entire corpus. **This now exists as a
first-party reference plugin** (`plugins/n8n-compat/`): the workflow-run→span mapping
is fixture-tested (workflow = trace, each node = a span, the node graph = the span
tree), proving the claim concretely rather than only rhetorically.

**Evidence (the economics).** Demand is a long tail of *target systems*, each a
plugin/normalizer in our model but a roadmap line item in a monolith. Ranked by
vote mass — this ranking **is our plugin roadmap ordered by market demand**:

| Rank | Target | Vote mass | Rank | Target | Vote mass |
| ---: | --- | ---: | ---: | --- | ---: |
| 1 | **n8n** | **384** | 7 | LiveKit (voice) | 42 |
| 2 | LangChain | 239 | 8 | Vercel AI SDK | 34 |
| 3 | LlamaIndex | 91 | 9 | DSPy | 31 |
| 4 | Prometheus | 59 | 10 | Spring AI | 27 |
| 5 | Warehouse export (BigQuery/Snowflake) | 50 | 11 | LiteLLM | 27 |
| 6 | AWS Bedrock | 50 | 12+ | Flowise, Pydantic AI, Dify, OpenAI Agents SDK, Google ADK, Haystack, Semantic Kernel, … | ≤23 each |

A monolith serves the head of that distribution and drops the tail; a plugin
ecosystem serves the tail by making the marginal integration *someone else's
afternoon*, not the core team's quarter. Full cluster breakdown in the
[Evidence appendix](#b-langfuse-discussions--demand-clusters-and-the-plugin-roadmap).

**Evidence (the "Not Planned" figure).** Of Langfuse's **25 most-upvoted closed
issues, 10 (40%) are closed `not_planned`** — including conditional prompt
rendering, dataset-item comments, auto-render images, `fetch_score` in the SDKs,
and per-cloud pricing rows. This is monolith economics: a single roadmap cannot fund
a long tail of niche asks, so most lose — whereas in a kernel-plus-plugins model each
is a community plugin that never queues behind a core team. The full ranked table,
the exact Search API query, and the mining date are in the
[Evidence appendix](#d-langfuse-issues--the-40-not-planned-figure), so the figure is
reproducible against the live tracker. It is exact for that defined set (top-25 closed
by votes) and is **not** a claim about all issues.

---

## Pillar 5 — Enterprise auth in the OSS core (shipped, not promised)

**Claim.** The auth capabilities both incumbents reserve for paid tiers — SSO/OIDC, RBAC,
provisioning, immediate revocation — ship in the LLMObs **Apache-2.0 core**, gated only by
authorization, never by a plan or a license key.

**Evidence (ours — shipped).** Arc O landed the full production-auth stack on `develop`:

- **`resource:verb` RBAC** with per-org memberships, resolved at the ONE Query-API
  intersection seam (O1 #123, O2 #124; ADR-0031/0032).
- **Provisioning** — invite / set-role / remove / create-org — under one shared gate,
  every role capped strictly-below the actor's own so a provisioning bug can't become
  account-takeover (O3 #125).
- **Immediate revocation** of every credential type (sessions, API keys, and the
  kernel-signed plugin tokens) on the very next request, with a user-revoke cascade
  (O4 #128, closes #63; ADR-0033).
- **OIDC/SSO** with strict verify-before-trust (signature vs the IdP JWKS, issuer,
  audience, expiry, nonce, and CSRF `state` all checked before any claim is read),
  group→role mapping that fails closed, and JIT provisioning that inherits the O3 gates
  (O5 #131; ADR-0034).
- **Per-user state** isolated by the server-resolved identity (O6 #132; ADR-0035).

Every one of these is proven by an adversarial prove-the-negative suite (44 test functions
across the arc), each written to fail if the boundary regresses.

**Contrast (fair, sourced).** SSO is **enterprise-gated at Opik/Comet** and Langfuse gates
its `admin-api`; the incumbents' reflex is to put org-level auth behind a plan tier (Opik
issues 7144/7123, above). LLMObs treats auth as *core, gated only by authz* — the self-hoster
who needs SSO + RBAC to go to production gets it in the OSS build, not on a sales call. Remaining
enterprise-auth surfaces (**SAML**, **SCIM**) are named, tracked follow-ons
([`backlog/README.md`](backlog/README.md) Cluster B), not paywalled features.

---

## Competitor-sourced structural wins (Opik cross-over mine)

Concrete examples where a bug a *second* incumbent (Opik/Comet) actually shipped is
**structurally impossible** for us — the strongest kind of positioning claim because it
is sourced from a competitor's own issue tracker, not our marketing:

- **The span-published-but-trace-lost race is impossible for us.** Opik #2782: spans
  persisted successfully but the *trace* was never published (an ADK/Vertex race),
  so the trace vanished from the dashboard despite a generated trace id. In LLMObs a
  trace is not a separately-published object — it is a **query-time materialized view
  derived from its spans** (DSL §4.1: min start_time, max end_time, root-span
  dimensions, any-error status, tag union). There is no separate trace write to race
  against a span write, so this entire failure class cannot occur. *This is the
  strongest single finding of the Opik mine — lead a reliability story with it.*
- **Feature-gating-by-plan is the incumbents' reflex, not ours.** A third independent
  data point (Opik #7144/#7123 gating "Cost Intelligence" on an org entitlement;
  alongside Langfuse's `admin-api` gating and Opik's enterprise-gated SSO) that the
  incumbents put value behind plan tiers. The LLMObs wedge: ungated core, gated only
  by authz — auth/RBAC is core, never an upsell. **This is now SHIPPED, not promised
  (see Pillar 5).**
- **Backpressure-as-503 is the right shape** — Opik #7091 independently arrived at
  fast-fail HTTP 503 on pool saturation, matching our G2 backpressure contract
  (`503` + `Retry-After: 1` → client retries into the idempotent merge). Convergent
  design from an unrelated codebase is evidence the contract is correct.

Provenance: every Opik item above is a public issue/PR in the
[`comet-ml/opik`](https://github.com/comet-ml/opik) tracker — cited by number so each
is checkable at `https://github.com/comet-ml/opik/issues/<n>`. Mined 2026-07-11 across
three rounds (~710 items triaged); method and cross-over rates in the
[Evidence appendix](#e-opik-cometthe-second-incumbent-cross-over-mine).

## What this is not

Not a claim that LLMObs is more mature than Langfuse today — it is younger and has
less built. These pillars are about *structural* advantages that compound as both
projects grow, not a feature-parity boast. The launch narrative should lead with
"the votes ratify decisions we already made" (two of the community's top-voted asks
— the `embedding` kind, [#1021](https://github.com/orgs/langfuse/discussions/1021),
91 votes; and session/categorical scores,
[#2728](https://github.com/orgs/langfuse/discussions/2728), 35 votes — shipped
before they were requested) and with the structural positions above, not with a
feature-count comparison we would lose.

---

## Appendix — Evidence base

The competitive claims above are computed from public competitor artifacts mined on
the dates below. This appendix records the **exact queries**, so every figure is
reproducible against the live trackers by anyone — no trust in our summary required.
Vote counts are point-in-time and drift upward; the *ratios and rankings* are the
claim, not the absolute numbers.

The raw mined datasets (a 3,470-discussion GraphQL dump and a 40-issue Search API
dump) are **not committed** — they are large, they go stale, and they are
regenerable from the queries below in minutes. What is preserved here is what the
claims actually rest on.

### A. Method and reproducible queries

| Corpus | Query | Mined | Scope |
| --- | --- | --- | --- |
| Langfuse discussions | GitHub GraphQL API via `gh api graphql`, all discussions in `langfuse/langfuse` | 2026-07-10, re-fetched 2026-07-11 | 3,470/3,470 fetched both passes (0 gap); the 07-11 refetch reproduced the same cluster ordering and vote-mass magnitudes — no cluster changed rank |
| Langfuse issues | GitHub Search API: `repo:langfuse/langfuse is:issue is:closed sort:reactions-+1-desc` | 2026-07-10 | top 40 fetched; the 40% figure is computed over the **top 25** |
| Opik issues + PRs | `gh issue list` / `gh pr list -R comet-ml/opik`, newest-first, 3 rounds | 2026-07-11 | ~710 items triaged (see §E) |

Clustering is over the **Ideas + Announcements** demand set (feature requests);
Support how-to questions are counted separately. Items are single-labelled to their
dominant cluster.

### B. Langfuse discussions — demand clusters and the plugin roadmap

Vote mass = summed 👍 across the cluster's discussions.

| Cluster | Items | Vote mass | Top items |
| --- | ---: | ---: | --- |
| **integrations/demand** | ~80 distinct | **~750+** | n8n #4397 (381), LlamaIndex #1291 (52), LiveKit #5235 (30), warehouse export #2131 (29) |
| evals/datasets | 377 | 1454 | prompt-experiment inputs #4454 (79), export run table #4077 (50), delete evaluators #7527 (39) |
| prompt mgmt | 189 | 810 | export/import #1696 (53), conditional #6010 (37), diff view #1105 (32) |
| UI polish / playground | 158 | 688 | multi-modal playground #6017 (71), tool calls #3166 (43) |
| trace integrity / data model | 33 | 485 | embedding type #1021 (91), full-text search #939 (75), delete session #1896 (58) |
| self-hosting / deploy | 36 | 381 | AWS #4645 (94), Azure #4647 (73), GCP #4646 (69) |
| SDK/instrumentation | 97 | 373 | LangChain-in-prompts #2237 (37), OTel ingest #2509 (21) |
| access/admin / RBAC | 47 | 320 | webhooks #1033 (109), admin API #1007 (41), api-key scopes #7104 (25) |
| cost/usage | 66 | 272 | alerts/limits #3997 (67), currency #2586 (20) |
| ClickHouse ops | 15 | 128 | Prometheus metrics #2508 (42), multi-shard #5021 (34) |

**Caveat (stated, not hidden):** single-labelling *under-counts* integrations —
n8n alone (381) dwarfs several whole clusters. Pulled together across every
integration/framework/target request, integrations is the largest demand cluster by
both item count and effective vote mass. The by-target ranking in Pillar 4 is the
honest view.

Note `evals/datasets` has the highest raw item count (377) *and* the two
highest-comment threads in the corpus (#9552, #9499 — both Support how-to
questions): evals are simultaneously the most-requested and the most-confusing
surface. That is a product signal for the evals plugin, not a positioning claim.

### C. Langfuse v4 — the migration cliff (discussion #12518)

[Discussion #12518](https://github.com/orgs/langfuse/discussions/12518) — "Upcoming
architecture changes: Simplify Langfuse for Scale (v4)", pinned Announcement,
19 votes / 14 comments (2026-07-10).

The architectural change, verbatim from the announcement:

> "We are moving to an **observation-centric data model** based on a new, **wide,
> (mostly) immutable ClickHouse table**. This eliminates joins and deduplication at
> read-time and optimizes for the most performant ClickHouse access patterns."

Rollout facts underpinning Pillar 2:

- v4 launched as Preview on Langfuse **Cloud 2026-03-10**.
- **@clemra (MEMBER, co-founder)**, 2026-05-01: *"V4 is already live in cloud and
  we're making sure it's a) stable and b) that there's a clear and well documented
  migration path… it's hard to guarantee release dates… This is top prio for us and
  we expect this to land in a few weeks."* — prompted by a user saying *"other tools
  are looking more shiny right now… it's starting to look like a better option."*
- **@Steffen911 (MEMBER)**, 2026-06-11: v4 self-host preview announced for **new
  deployments only** (#14157).
- Self-hosters asking for an existing-deployment migration ETA, with vote counts:
  timur-3c (16), savitha-suresh (14), ashgold (14), arunkumar-maker (6).
- Older SDKs → up to **10-minute data delay** in the new UI; real-time requires
  Python SDK v4 / JS-TS SDK v5 (an upgrade tax coupled to the migration).
- @kasuteru flags semantic regressions from immutability: `GET /traces/{traceId}`
  now wants a timeframe the caller may not have; trace-level scoring/comment
  semantics unclear.

**Why this is the anti-pattern, stated fairly:** the v3 two-store (Postgres +
ClickHouse) model was join-heavy at read time, and v4's pitch is eliminating that.
The problem is not that they improved it — it is that a physical-layout change
became a *product-wide migration self-hosters must survive*, because the storage
layout was not behind an adapter contract. That is the exact coupling our storage
seam exists to prevent.

### D. Langfuse issues — the "40% Not Planned" figure

Query: `repo:langfuse/langfuse is:issue is:closed sort:reactions-+1-desc` (GitHub
Search API), mined 2026-07-10, top 40 fetched.

**Of the top 25 closed issues by 👍, 10 are `state_reason: not_planned` = 40%.**
(The ratio holds at top-30: 12/30.) These are not stale or low-signal asks — they
are among the most-upvoted issues in the tracker, and the incumbent declined them.

| Issue | 👍 | Title | Closed as |
| --- | ---: | --- | --- |
| [#9618](https://github.com/langfuse/langfuse/issues/9618) | 49 | Incompatibility with Python 3.14 | completed |
| [#6906](https://github.com/langfuse/langfuse/issues/6906) | 19 | Conditional rendering in prompt management | **not planned** |
| [#11109](https://github.com/langfuse/langfuse/issues/11109) | 19 | Experiments/Evaluators fail to parse Anthropic "thinking" blocks | **not planned** |
| [#9473](https://github.com/langfuse/langfuse/issues/9473) | 18 | LangChain JS: set spans active in OTel context | **not planned** |
| [#6588](https://github.com/langfuse/langfuse/issues/6588) | 15 | Integration guide for Agent Development Kit (ADK) | completed |
| [#2169](https://github.com/langfuse/langfuse/issues/2169) | 13 | Add `py.typed` to python sdk (mypy typecheck) | **not planned** |
| [#5704](https://github.com/langfuse/langfuse/issues/5704) | 12 | Dynamic imports causing errors when testing with Jest | completed |
| [#8780](https://github.com/langfuse/langfuse/issues/8780) | 12 | "Failed to detach context" with async LangChain CallbackHandler | completed |
| [#3961](https://github.com/langfuse/langfuse/issues/3961) | 11 | FastAPI StreamingResponse breaks tracing context | completed |
| [#5486](https://github.com/langfuse/langfuse/issues/5486) | 11 | Comments on dataset items | **not planned** |
| [#5142](https://github.com/langfuse/langfuse/issues/5142) | 11 | Setting to auto-render all images inline | **not planned** |
| [#6572](https://github.com/langfuse/langfuse/issues/6572) | 10 | Langfuse v3 ClickHouse CPU consumption | completed |
| [#5057](https://github.com/langfuse/langfuse/issues/5057) | 9 | Ability to delete an evaluator (LLM-as-a-judge) | completed |
| [#6090](https://github.com/langfuse/langfuse/issues/6090) | 9 | Export dataset items in UI as CSV/JSON | completed |
| [#9770](https://github.com/langfuse/langfuse/issues/9770) | 9 | Add LangChain v1 support | completed |
| [#7122](https://github.com/langfuse/langfuse/issues/7122) | 9 | Bedrock configuration requires static AWS credentials | completed |
| [#1874](https://github.com/langfuse/langfuse/issues/1874) | 8 | Langfuse was not able to parse the LLM model | completed |
| [#2734](https://github.com/langfuse/langfuse/issues/2734) | 8 | LlamaIndex: model/token counter expects `response.raw` as dict | completed |
| [#7152](https://github.com/langfuse/langfuse/issues/7152) | 8 | Failure to use `HTTPS_PROXY` for outbound Playground calls | completed |
| [#8216](https://github.com/langfuse/langfuse/issues/8216) | 8 | `@observe` + FastAPI StreamingResponse splits traces | completed |
| [#1911](https://github.com/langfuse/langfuse/issues/1911) | 7 | Log for function-calling in LangChain | **not planned** |
| [#2785](https://github.com/langfuse/langfuse/issues/2785) | 7 | Add Gemini-1.5 input/output prices to the model table | **not planned** |
| [#3380](https://github.com/langfuse/langfuse/issues/3380) | 7 | Add `fetch_score(s)` in the SDKs | **not planned** |
| [#9257](https://github.com/langfuse/langfuse/issues/9257) | 7 | Support `gen_ai.system_instructions` (OTel gen-ai semconv) | **not planned** |
| [#517](https://github.com/langfuse/langfuse/issues/517) | 6 | Allow deployment with a given API token | completed |

**Reading (structural, no FUD):** this is monolith economics, not laziness. Every
request — however niche — lands on one core team's single roadmap, and most must
lose. Several of the declined items are exactly what a kernel-plus-plugins model
makes *someone else's* afternoon: conditional prompt rendering is a prompt-plugin
feature; auto-render-images is a tracing-plugin setting; per-cloud pricing rows are
a cost-plugin table row.

**Secondary finding — the SDK-ownership treadmill.** Roughly half the top closed
issues are the maintenance cost of owning instrumentation SDKs across a churning
framework ecosystem: Python 3.14 incompatibility (#9618, the single most-upvoted),
plus a recurring async/streaming context-propagation failure class
(#3961, #8780, #8216, #9473, #5704, #2169, #9770). Our no-SDK / OTLP-canonical decision outsources
this to the far larger OTel ecosystem. **What survives for us is a fixture
obligation, not an SDK:** streaming/async is where upstream instrumentation breaks,
so broken-parentage and split-trace inputs must stay in the normalizer fixture set
(handled by orphan-promotion + `incomplete_trace`). And #9257
(`gen_ai.system_instructions`, declined) validates a structural advantage: because
we preserve raw attributes alongside canonical fields, a lagging normalizer loses
nothing — it only delays promotion, never data.

### E. Opik (Comet) — the second-incumbent cross-over mine

Three rounds against [`comet-ml/opik`](https://github.com/comet-ml/opik) (Java/
Dropwizard + ClickHouse + MySQL — architecturally unlike Langfuse, which is what
makes agreement between them meaningful), mined 2026-07-11, ~710 items triaged.

| Round | Window | Items | New cross-over rate |
| --- | --- | ---: | ---: |
| 1 | PRs #7254→#7426; issues → #4136 | ~250 | ~5.6% |
| 2 | PRs #7091→#7248; issues #2769→#4137 | ~240 | ~5.4% |
| 3 (closeout) | PRs #6908→#7059; issues #2013→#2782 | ~220 | **~1.8%** |

**The collapse is the finding, and the mine is closed.** By Round 3 nearly every
"hit" merely re-confirmed a rule already pinned from Rounds 1–2 — two
independently-architected incumbents had converged on the same handful of hard
surfaces, and we had banked immunity or a rule for each. Round 3's cost findings
were a stream of *one-provider-at-a-time* cache/cost patches
(Opik #7016, #6980, #6971, #6978, #7023, #7037), which is itself the empirical proof of the meta-lesson
behind our cost rules: **enumerating special cases always leaves an un-enumerated
case falling through to the flat rate — price by iterating the price entry's rate
fields instead.**

Where the mine's rulings landed (all substance is now normative in-tree, which is
why the raw reports are not retained):

| Finding class | Durable home |
| --- | --- |
| Cost derivation: cache/audio own-rate, data-driven not allow-listed; tiered pricing; symmetric model-key + provider canonicalization; reduce-then-tier | `api/model/v1alpha1/README.md` §7.1–7.7 (normative) — implemented in `kernel/internal/costderive` |
| Aggregate spans must not double-count leaf usage (**both** incumbents: Langfuse #14808, Opik #4695 — the strongest signal in the corpus) | §7.1 + standing conformance vector |
| 429 `insufficient_quota` = permanent vs 429 rate-limit = transient | CLAUDE.md invariant 12; ADR-0027 |
| ClickHouse OOM caps; never-literal cluster macros in migrations | ADR-0026 |
| Airgap must survive a vanished/relicensed upstream image; Helm-values coverage; image hygiene | `.claude/rules/deploy.md` |
| Auth: permission intersection at the ONE seam; identity never re-derived per caller; ungated-by-plan/strictly-gated-by-authz | ADR-0031–0035 (Arc O, shipped — Pillar 5) |

Structural immunities confirmed by the mine are in "Competitor-sourced structural
wins" above. One worth recording here because it never became a code change:
**Opik #5619** — a fallback path read a `total_tokens` *count* (150,000) and
returned it as **$150,000**. We are immune by construction: `provided_cost_details`
(typed decimals) and usage counts are separate maps, so a token count can never be
read as a cost.
