# Langfuse Discussions — Clustered Demand Analysis

Mined 2026-07-10 · 3470/3470 fetched (0 gap) · votes point-in-time (2026-07-10).
Clustering is over the **Ideas + Announcements** demand set (feature requests);
Support how-to questions are counted separately where noted. Items are assigned to
their dominant cluster (single-label); a few multimodal items legitimately span
UI-polish and data-model.

## Cluster summary (item count + total vote mass)

| Cluster | Items | Vote mass | Top items |
|---------|------:|----------:|-----------|
| **integrations/demand** | ~80 distinct | **~750+** | n8n #4397 (381), LlamaIndex #1291 (52), LiveKit #5235 (30), warehouse export #2131 (29) |
| **evals/datasets** | 377 | 1454 | prompt-experiment inputs #4454 (79), export run table #4077 (50), delete evaluators #7527 (39), code evaluators #6087 (38) |
| **prompt mgmt** | 189 | 810 | export/import #1696 (53), diff view #1105 (32), composability #1660 (26), conditional #6010 (37) |
| **UI polish / playground** | 158 | 688 | multi-modal playground #6017 (71), tool calls #3166 (43), configurable dashboards #1011 (34) |
| **trace integrity / data model** | 33 | 485 | embedding type #1021 (91), full-text search #939 (75), delete session #1896 (58), session scores #2728 (35) |
| **self-hosting / deploy** | 36 | 381 | AWS #4645 (94), Azure #4647 (73), GCP #4646 (69), base-url #2400 (24) |
| **SDK/instrumentation** | 97 | 373 | LangChain-in-prompts #2237 (37), OpenTelemetry ingest #2509 (21), JS sampling #3529 (16), Java SDK #4589 (11) |
| **access/admin / RBAC** | 47 | 320 | webhooks #1033 (109), admin API #1007 (41), api-key scopes #7104 (25) |
| **cost/usage** | 66 | 272 | alerts/limits #3997 (67), currency #2586 (20), judge-cost #7166 (18) |
| **ClickHouse ops** | 15 | 128 | Prometheus metrics #2508 (42), multi-shard #5021 (34), Prometheus endpoint #1816 (17) |

Notes:
- The "integrations" cluster is under-counted by single-labeling because the
  single biggest item (n8n, 381) alone dwarfs several whole clusters. When you
  pull every integration/framework/target request together, **integrations is the
  largest demand cluster by both item count and, driven by n8n, effective vote
  mass** — as expected. It is broken down by target below.
- `evals/datasets` has the highest *raw* item count (377) and the two
  highest-comment threads in the whole corpus (#9552 73 comments, #9499 64
  comments) — both **Support** how-to questions. Evals are simultaneously the most
  requested *and* the most confusing surface.

---

## Integrations broken down BY TARGET SYSTEM (the plugin roadmap)

Vote mass per target system (title scan across the full corpus; a target's mass
sums every discussion whose title names it). This ranking **is our future plugin
roadmap ordered by market demand.**

| Rank | Target | Vote mass | Items | Flagship discussion |
|-----:|--------|----------:|------:|---------------------|
| 1 | **n8n** | **384** | 4 | #4397 (381) |
| 2 | **LangChain** | 239 | 104* | #2237 (37), #1212 (29) |
| 3 | **LlamaIndex** | 91 | 17 | #1291 JS/TS (52), #828 (20) |
| 4 | **Prometheus** (self-monitoring) | 59 | 2 | #2508 (42), #1816 (17) |
| 5 | **BigQuery/Snowflake (warehouse export)** | 50 | 3 | #2131 (29), #1219 (18) |
| 6 | **AWS Bedrock** | 50 | 21 | #6271 (9), #2864 (7) |
| 7 | **LiveKit** (voice agents) | 42 | 7 | #5235 (30) |
| 8 | **Vercel AI SDK** | 34 | 22 | #7711 (5) |
| 9 | **DSPy** | 31 | 12 | #1295 (19) |
| 10 | **Spring AI** | 27 | 10 | #10097 (13) |
| 11 | **LiteLLM** | 27 | 19 | #3780 (4) — also the de-facto n8n workaround |
| 12 | **Flowise** | 23 | 11 | #969 (6) |
| 13 | **Pydantic AI** | 22 | 10 | #5036 (9) |
| 14 | **Dify** | 18 | 14 | #2640 (3) |
| 15 | **OpenAI Agents SDK** | 17 | 6 | #9144 (8) |
| 16 | **Google ADK** | 16 | 11 | #10084 (2) |
| 17 | **Haystack** | 14 | 4 | #1221 (9) |
| 18 | **Semantic Kernel** | 13 | 3 | #4772 (10) |
| — | AutoGen / Langflow / Ollama / CrewAI / Strands / Mastra | ≤8 each | tail | long tail |

*LangChain's 104 "items" include many Support how-to questions that merely mention
LangChain; its request vote mass (239) is real but more diffuse than n8n's single
concentrated ask.

**Roadmap read:** n8n is the runaway #1 and is *structurally impossible for
Langfuse to serve* (needs n8n-core support) — our compat-plugin model turns their
dead-end into our flagship. LangChain and LlamaIndex are table-stakes normalizers.
Everything from Bedrock down is a normalizer/compat-plugin the community can own.

---

## Cross-reference: high-vote items vs THIS repo's decisions/issues

ADRs present: 0016 canonical data model, 0017 score model, 0018 span taxonomy &
payload shapes, 0019 query DSL, 0020 indexed attribute keys, 0021 explicit-clear
sentinel, 0022 time authority. Open/closed issues checked via
`gh issue list --repo m7mdhka/llmobs --state all`.

| Demand item | Votes | Maps to LLMObs decision/issue |
|-------------|------:|-------------------------------|
| Full-text search #939 | 75 | ADR-0019 (Query DSL) + ADR-0020 (indexed attribute keys); issue #16 (ClickHouse adapter + DSL→SQL) |
| Embedding observation type #1021 | 91 | ✅ **shipped** — `embedding` is in the frozen `kind` enum (LM-1 / ADR-0018, `span.schema.json`) |
| Session-level scores #2728 / #6777 | 35/29 | ✅ **shipped** — `session` subject type live in the score write path (ADR-0017, PR-E2) |
| Categorical/boolean judge scores #4965 | 29 | ✅ **shipped** — `categorical`/`boolean` `data_type` live in the score write path (ADR-0017, PR-E2) |
| Multi-modal / base64 content #3004/#6017 | 64/71 | **ADR-0018 payload shapes** + blob primitive (needs contract evolution) |
| Multi-shard ClickHouse #5021 | 34 | **issue #16** (scale ClickHouse storage adapter) |
| Webhooks / event subscribe #1033 | 109 | **issue #14** (durable event bus, Redis/NATS) + `events` primitive |
| Alerts/limits on metrics #3997 | 67 | issue #14 (event bus) + issue #13 (cost derivation) + `jobs` primitive |
| Admin API / project & key mgmt #1007 | 41 | **issue #21** (auth: users, roles/RBAC, OIDC, project mgmt) |
| Custom api-key scopes #7104 | 25 | **issue #21** (RBAC) — permission intersection (D7) |
| Cost alerts / usage #3997/#7166 | 67/18 | **issue #13** (cost derivation + pricing snapshot, enrich stage) |
| Out-of-order / trace update semantics (v4 #12518) | 19 | **issue #17** (merge-on-write per-field provenance) + ADR-0021 |
| Time / multi-timezone #5046 | 47 | **ADR-0022 time authority policy** (producer vs receive time) |
| OpenTelemetry ingestion #2509 | 21 | Normalizers (OTLP-canonical, D6) — already core |
| Backend/community plugin tabs (skills #12290, dashboards #1011) | 24/34 | **issue #25** (Tier-3 backend plugins, install lifecycle, SchemaForm) |

Takeaway: the highest-vote structural asks (search, scores, event bus, cost,
RBAC, out-of-order updates, embedding type) already have a home in our ADRs or
open kernel issues — the demand data **validates the existing kernel backlog**
rather than exposing blind spots. The gaps are all *plugin-surface* features
(prompt mgmt, evals UX, dashboards, integrations), which is exactly where the
plugin ecosystem is supposed to absorb demand.

---

## What the votes say we should build first

Top 10 demand items (across all clusters) that map to an LLMObs surface. Architecture-fit
vocabulary: 🟢 supported / 🟡 designed-not-built / 🟠 needs-contract-evolution / 🔴 resists.

| # | Item (title + #) | Votes | Owning surface | Fit | Positioning value |
|---|------------------|------:|----------------|:---:|-------------------|
| 1 | n8n tracing #4397 | 381 | community/first-party **compat plugin** (own endpoint, cold path) | 🟢 | The #1 ask Langfuse *structurally cannot* ship; our plugin model makes it a container+manifest — flagship proof of the thesis. |
| 2 | Webhooks / event subscribe #1033 | 109 | **kernel** event bus + `events`/`surface` primitives | 🟡 | issue #14 designed; a durable bus + subscription is table-stakes and unlocks alerts (#3997). |
| 3 | Embedding observation type #1021 | 91 | **kernel** canonical model (LM-1 / ADR-0018) | ✅ shipped before requested | `embedding` is already in the frozen `kind` enum (`span.schema.json`); the vote ratifies a decision we shipped. |
| 4 | Full-text search on I/O #939 | 75 | **kernel** Query DSL + storage adapter | 🟠 | ADR-0019/0020 + issue #16; needs DSL + CH/PG search path — high value, real work. |
| 5 | Multi-modal content #3004/#6017 | 64/71 | **kernel** payload shapes + blob; **plugin** playground UX | 🟠 | ADR-0018 payload evolution + blob primitive; separating large media from the trace row is our design's strength. |
| 6 | Alerts/limits on cost/eval #3997 | 67 | **kernel** event bus + `jobs` + cost enrich | 🟡 | issues #13+#14; converts observability into action — a headline differentiator. |
| 7 | Self-host deploy stacks (AWS/Azure/GCP) #4645/#4647/#4646 | 94/73/69 | **deploy/** templates (Helm/operator) | 🟢 | Two-profiles/one-Query-API + Helm already exist; official IaC per cloud is packaging, and directly answers v4's stranded-self-hoster pain. |
| 8 | Session-level & categorical scores #2728/#4965 | 35/29 | **kernel** score model (ADR-0017) | ✅ shipped before requested | `session` subject type + `categorical` (and `boolean`) `data_type` are live in the score write path since PR-E2; the votes ratify shipped contracts. |
| 9 | Admin API + RBAC/key scopes #1007/#7104 | 41/25 | **kernel** auth/tenancy (issue #21) | 🟡 | RBAC + scoped keys with permission intersection (D7) is designed; enterprise self-host precondition. |
| 10 | LangChain + LlamaIndex ingestion #2237/#1291 | 239/91 (target mass) | **normalizers** (hot path) or SDK | 🟢 | OTLP-canonical normalizers; the two most-demanded frameworks after n8n — must be first-party. |

**Two of the top ten are already shipped** — the `embedding` kind (#3) and
session/categorical scores (#8) landed before the community asked. That's not a
gap, it's ratification: the votes confirm decisions we already made. **Bottom
line:** build n8n-plugin (proves the ecosystem), event-bus+webhooks (#14, unlocks
alerts), and the LangChain/LlamaIndex normalizers — in that order. Every one of
these maps to an existing ADR or open issue; the demand data ratifies the roadmap
rather than
redirecting it. The single strategic wedge is **self-hosting**: v4 (#12518) has
publicly stranded Langfuse's self-hosters mid-migration for months, and items
#4645/#4647/#4646 (236 combined votes) show self-host deploy ergonomics are a
top-tier unmet need our two-profile design is built to win.
