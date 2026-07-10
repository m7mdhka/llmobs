# #12518 — "Upcoming architecture changes: Simplify Langfuse for Scale (v4)"

- URL: https://github.com/orgs/langfuse/discussions/12518
- Category: Announcements (pinned) · Author: langfuse maintainer
- Votes (2026-07-10): **19** · Comments: **14**

## Full body (verbatim summary)

Launched as Preview on Langfuse Cloud **March 10, 2026**. The stated goal: *"making
Langfuse much faster at scale."* Claims for v4: faster chart loading, faster
trace/user/session browsing, new **Observations API v2** and **Metrics API v2**
that "perform much better on large projects," and observation-level evaluations
that "execute in seconds without a ClickHouse query per evaluation."

### The core architectural change

> "We are moving to an **observation-centric data model** based on a new, **wide,
> (mostly) immutable ClickHouse table**. This eliminates joins and deduplication
> at read-time and optimizes for the most performant ClickHouse access patterns."

### Rollout status
- v4 live on **Cloud** as public Beta, reversible Preview toggle.
- **OSS / self-hosted migration path still being built** — separate self-host
  preview only for *new* instances (discussion #14157, docs added 2026-06-11).
- Migration note: older SDKs cause **up to 10-minute data delay** in the new UI;
  real-time requires Python SDK v4 / JS-TS SDK v5.

## Maintainer comments (all substantive ones)

- **@clemra (MEMBER, co-founder), up=5 rx=10 (2026-05-01):** "V4 is already live
  in cloud and we're making sure it's a) stable and b) that there's a clear and
  well documented migration path… it's hard to guarantee release dates… This is
  top prio for us and we expect this to land in a few weeks." Prompted by a user
  saying "other tools are looking more shiny right now… it's starting to look
  like a better option."
- **@Steffen911 (MEMBER), up=3 rx=5 (2026-06-11):** announced a v4 self-host
  preview for **new** deployments only (#14157).

## The pain v4 admits to (competitive intel)

1. **The two-store v3 model (Postgres + ClickHouse) was join-heavy and slow at
   read time.** v4's whole pitch is eliminating read-time joins and dedup. This
   is a direct admission that the v3 architecture — which our flexibility audit
   catalogues as a weakness — did not scale on its own read patterns.
2. **Per-evaluation ClickHouse queries were a real cost.** "Observation-level
   evaluations now execute in seconds without a ClickHouse query per evaluation"
   confirms evals were architecturally bolted onto the trace store, not a
   first-class async pipeline.
3. **Migration weight is severe.** A wide, mostly-immutable table means the v3→v4
   move is a full data-model migration, not a schema patch. Cloud shipped in
   March; **self-hosted existing deployments still had no migration path as of
   late June** (comments from savitha-suresh up=14, timur-3c up=16, ashgold
   up=14, arunkumar-maker up=6 all asking "ETA for OSS/self-host?").
4. **Self-hosting complexity is the sore spot.** The loudest, most-upvoted
   comments are self-hosters stranded: they can adopt v4 only on brand-new
   instances, and the co-founder concedes users are eyeing competitors while they
   wait. SDK-version coupling (v4/v5 required for real-time) adds an upgrade tax.
5. **Immutability changes semantics users relied on.** @kasuteru (up=2) flags two
   regressions: (a) `GET /traces/{traceId}` now wants a **timeframe** the user may
   not have, and (b) trace-level scoring/comments semantics are unclear under an
   observation-centric model ("will a trace score show up on every observation?").
   Moving to observation-centric breaks the trace-as-unit mental model.

## What it implies about weaknesses we've catalogued

- **Two-store split (Postgres + ClickHouse):** v4 doesn't remove ClickHouse — it
  doubles down on it with a wide immutable table and pushes even more onto CH read
  patterns. The Postgres+CH operational burden for self-hosters is unchanged;
  the fix is CH-schema-shaped, not a simplification of the deployment topology.
- **Migration weight:** confirmed as a first-order problem — a public,
  co-founder-acknowledged, months-long gap between Cloud and self-host.
- **Self-hosting complexity:** the single biggest source of user anxiety in the
  thread. New-instances-only preview means existing self-hosters face a
  data-migration cliff.

## Positioning for LLMObs

Our microkernel + storage-adapter design is the structural answer to exactly this
thread. Because ingestion is OTLP-canonical and storage is an adapter (Postgres
in **lite**, ClickHouse in **scale**, ADR-0016 canonical model), a schema
evolution like "wide observation-centric table" is an adapter/DSL-compiler change
behind the Query API (issue #16), **not** a product-wide migration users must
survive. Two profiles / one Query API means self-hosters are never stranded on a
"new instances only" preview. This is our strongest competitive wedge: *Langfuse
had to rewrite its data model and still can't migrate its self-hosters months
later; LLMObs isolates that churn behind a contract.*
