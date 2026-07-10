# Flexibility Audit — Wave 3 (Stories + Evaluation)

Evaluated against `develop` @ `abc0156` (2026-07-10). **No code was changed** — this
is a read-only audit. Every verdict cites `file:line` from the current tree.

> **Set-B re-color, Arc H then Arc J — the honesty statement.** See the two
> "Set-B re-color" sections at the bottom. Arc H's eight PRs moved most of Set B and
> left **three yellows** (B2/B4/B9), all held by the deferred frontend-direct
> identity + DX. **Arc J closed those three** (J1 frontend token, J2 schema-form
> settings, J3 `make dev` + SDK test utils) — Set B is now all 🟢 — and names the
> **new** yellows honestly (frontend origin isolation, per-user settings, CLI
> breadth, coarse RBAC). A table with no yellows would mean the audit went easy on
> itself; the yellows just moved up a level.

Wave 3 has two story sets:
- **Set A — deployment-under-fire** (A1–A10): the platform under operational stress.
- **Set B — plugin-author's-life** (B1–B10): what a plugin author can actually build.

---

## Rubric

**Architecture-fit vocabulary** (shared with the demand audit in
[`langfuse-discussions/clusters.md`](langfuse-discussions/clusters.md)):

| Symbol | Meaning |
|:--:|---|
| 🟢 | **supported** — works today in real code, or is a trivial packaging step over real code |
| 🟡 | **designed-not-built** — the contract/seam exists and is coherent; the implementation does not |
| 🟠 | **needs-contract-evolution** — the current *or designed* shape is insufficient; the contract itself must change (additively) |
| 🔴 | **resists** — the architecture as specified cannot express this without a new primitive/decision |

**Per-story required fields.**

*Set A (each story):*
- **Blast radius** — what breaks, and how far it spreads (one node / one tenant / one plugin / whole cluster / data at rest).
- **Is acknowledged data lost?** — an explicit **YES / NO / N/A** for data the system already returned `200/OK` for. "N/A" only when the scenario cannot occur today (subsystem absent) — say so.

*Set B (each story):*
- **Primitives exercised** — which of the seven (`query, write, events, jobs, kv, secrets, surface`) the story requires.
- **Does the primitive's current-or-designed shape suffice?** — judged against the *designed* contract, with build-status noted.

> Ground-truth note carried into every verdict: the **runtime kernel** (OTLP ingest →
> normalize → redact → persist → query, health, metrics, migrations, graceful shutdown,
> idempotent merge, GDPR erasure, scoped API keys, row-level tenancy) is genuinely
> implemented on Postgres. **Everything orchestration- and plugin-backend-shaped**
> (Helm/operator/airgap, the supervisor + all four executors, the entire `llmobs` CLI,
> the ClickHouse/scale adapter, the event bus beyond `NoopBus`, and the `write/events/
> jobs/kv/secrets/ingest` primitives) is **designed-only or absent**. Verdicts reflect
> that split honestly rather than scoring absent code as passing.

---

## Set A — Deployment-under-fire

### A1 — Lite Postgres disk fills during sustained ingest
Postgres partition hits 100% mid-stream; `INSERT` starts failing but the connection pool still connects.
- **Reality:** ack precedes persist (`ingest/receiver.go:70-78` returns 200 at enqueue). A worker later calls `PersistSpan`, which now errors on the full disk; `/readyz` only pings the pool (`platform/health.go:20-35`) — a *connectable-but-unwritable* DB still reads "ready", so the node keeps returning 200 while every drained job fails to persist.
- **Blast radius:** one node, but silent — the load balancer keeps routing because readiness is green; every span acked during the outage is dropped with no client signal.
- **Is acknowledged data lost?** **YES.** Client got 200; the write failed post-ack and is not retried (client won't retry a success).
- **Fit:** 🟠 — needs contract evolution: persist-failure must propagate to backpressure (ack→503) and/or a write-health readiness gate. Ack-before-durable is the root (see A6).

### A2 — Rolling upgrade applies a migration while the old replica still serves
New replica boots, acquires `pg_advisory_lock`, applies migration N; old-code replica keeps serving against the new schema during the window.
- **Reality:** migrations are advisory-locked, per-file transactional, forward-only, auto-on-boot (`storage/postgres/migrate.go:23-92`, `cmd/llmobsd/main.go:64-68`). Additive-only + idempotent-DDL convention (`migrations/README.md:22-24`) keeps old code working *if* migrations are additive.
- **Blast radius:** none for additive migrations; a **non-additive** migration (rename/drop) would break the old replica's queries for the overlap window — and pre-`v0.1.0` migrations are *explicitly permitted to be destructive* (`migrations/README.md:9-18`). No supervisor/blue-green enforces additive-during-rollout.
- **Is acknowledged data lost?** **NO** (migrations are transactional; ingest continues; merge is idempotent).
- **Fit:** 🟡 — the DB-convergence half is real; the "never break a live old replica" guarantee is policy-by-convention, not enforced. Becomes 🟠 the day a destructive migration is needed.

### A3 — Scale profile: ClickHouse unavailable mid-ingest
A ClickHouse shard goes down during scale-profile ingest.
- **Reality:** the ClickHouse adapter does not exist — `storage/clickhouse/` is a `.gitkeep` (per the storage audit); `storage.go` calls this the seam "documented but absent in code." There are no Helm/operator artifacts to deploy scale at all.
- **Blast radius:** N/A — the entire scale profile is DESIGNED-ONLY; this scenario cannot be exercised.
- **Is acknowledged data lost?** **N/A** (no scale deployment exists).
- **Fit:** 🟡 — designed-not-built. Honest flag: the "works in both profiles" invariant is currently satisfied only in the lite direction (issue #16).

### A4 — Shared plugin runtime OOMs and restarts (lite)
The shared multi-plugin host dies under memory pressure.
- **Reality:** there is no shared backend host process — `runtimes/shared/` is a README only; today a plugin is a **frontend** Module-Federation remote (`plugins/tracing` is frontend-only). No plugin code sits in the ingest or persist path.
- **Blast radius:** today, a failed plugin degrades exactly one UI tab; the shell is expected to render the unavailable-state (SDK degraded-state components). Kernel ingest/query are untouched.
- **Is acknowledged data lost?** **NO** (plugins are off the data path).
- **Fit:** 🟡 — frontend isolation is real; the backend runtime whose OOM this story imagines is designed-not-built (issue #25). Isolation *by construction* today, blast-radius containment *to be proven* when backends land.

### A5 — Operator applies a manifest referencing a secret that doesn't exist
A plugin manifest wants `secretRef: n8n-api-key`; the secret was never created.
- **Reality:** there is **no secrets subsystem** and **no secret-reference field in the manifest schema** (`api/schemas/manifest/v1alpha1` has `capabilities/permissions/frontend/settingsSchema`, no secret refs). The operator that would apply it is absent.
- **Blast radius:** N/A today; but note the *designed* manifest cannot even express a secret dependency, so a future operator has nothing to validate against.
- **Is acknowledged data lost?** **NO.**
- **Fit:** 🟠 — needs contract evolution: both a `secrets` provider and a manifest secret-reference schema. This is a real gap, not merely unbuilt code.

### A6 — SIGTERM with a full ingest buffer (the every-deploy case)
Kernel pod receives SIGTERM (rolling deploy / scale-down) with the ingest queue non-empty.
- **Reality:** graceful shutdown is real (`main.go:170-186`), **but** workers return the instant `rootCtx` cancels (`receiver.go:105-106`) and `Stop()` only `wg.Wait()`s (`receiver.go:116`) — **the queue is never drained.** Up to `queueSize` (1024/4096) already-acked jobs are discarded on every clean shutdown.
- **Blast radius:** one pod, **on the routine happy path** — every rolling deploy and every autoscale-down silently drops a bounded batch of acked spans. This is not a crash-only edge; it fires on normal operations.
- **Is acknowledged data lost?** **YES** — and predictably, not just under fault.
- **Fit:** 🟠 — but the cheapest, highest-value fix in the whole set: drain the queue before workers exit and have `Stop()` flush. Real code, small change. **(Pull-forward candidate — see below.)**

### A7 — Airgap install: a plugin image/asset is missing from the bundle
The offline bundle is installed in a network-isolated site; a referenced asset isn't in it.
- **Reality:** `deploy/airgap/` is a README only — no bundle build, no manifest, no digest pinning. The one real building block is the registry's per-asset integrity hash (`registry/dirsource.go`), which *would* detect a corrupt/missing bundled asset.
- **Blast radius:** N/A today (no airgap tooling); designed intent is "no external URLs at install time."
- **Is acknowledged data lost?** **NO** (install-time, pre-data).
- **Fit:** 🟡 — designed-not-built; the integrity-hash primitive is a genuine head-start.

### A8 — Lite event bus wiped; events published while a subscriber was down
Redis (lite bus) is restarted/flushed; an alerting plugin was offline during the gap.
- **Reality:** the only bus implementation is `NoopBus`, whose `Publish` discards everything (`pipeline/eventbus.go:9-16`); `publishStage` ignores the return (`stages.go:242-245`). Redis/NATS backends are absent. Designed semantics (durable consumer groups + dead-letter) live only in `api/schemas/events/v1alpha1/README.md`.
- **Blast radius:** every event-*derived* side effect (alerts, webhooks, eval triggers) silently misses events — today unconditionally, not just when Redis is wiped. **The telemetry itself is safe:** publish is *after* persist, so trace/span rows are already durable.
- **Is acknowledged data lost?** **NO** for the acknowledged telemetry; event *deliveries* are lost (but nothing was acked to a client for those).
- **Fit:** 🟡 — designed-not-built (issue #14). The designed at-least-once + DLQ shape would suffice; the seam (`EventBus`/`publishStage`) is real and correctly placed.

### A9 — GDPR erasure lands concurrently with a re-delivery of the same trace
An erase request deletes user U's spans in `[from,to]`; a producer re-sends one of those spans moments later.
- **Reality:** erasure is a **hard** `DELETE` in one transaction with an audit row (`storage/postgres/erase.go:16-47`, `0007_erasure_audit.sql`) — *not* a tombstone. The merge path is an idempotent `SELECT … FOR UPDATE` upsert (`store.go:32-103`). A re-delivered span with the same id after the delete simply **re-inserts** — the erased data is resurrected.
- **Blast radius:** compliance-critical: erased personal data can silently reappear if any producer retries after erasure; the audit row now overstates what was actually removed.
- **Is acknowledged data lost?** **NO** — the inverse: data that was *supposed* to be erased is **not** durably gone. A correctness/compliance hole, not a loss.
- **Fit:** 🟠 — needs contract evolution: an erasure suppression list / tombstone that the persist stage consults so re-ingest of an erased key is refused (or re-erased). Small table + one predicate.

### A10 — GitOps drift: someone hand-edits a plugin container; supervisor reconciles
An operator manually mutates a running plugin; the desired state should win.
- **Reality:** the supervisor and all four executors (cli-compose/operator/gitops/external-url) are absent — no reconcile loop exists (`controlplane/` has no `executors`/`supervisor`). The "kernel never touches the Docker/K8s socket" invariant holds, but only because no orchestration code exists yet.
- **Blast radius:** N/A today — a hand edit simply persists; nothing reconciles it.
- **Is acknowledged data lost?** **NO.**
- **Fit:** 🟡 — designed-not-built. The invariant is safe by absence, which is worth stating plainly rather than scoring as a win.

---

## Set B — Plugin-author's-life

> SDK reality check (applies throughout): of the seven primitives, only **`query`** is
> usable end-to-end and only **`surface`** (frontend tab, via Module Federation) works;
> **`write`** is scores-only and *not exposed in the SDK client* (raw HTTP `POST
> /v1alpha1/scores`); **`events`, `jobs`, `kv`, `secrets`** (and the newer `ingest`
> capability) are enum strings with no backing code. The double-token permission
> intersection is named but **not computed** — the Query API grants fixed full scopes to
> the admin session (`dataplane/query/server.go:55`). Per the rubric, sufficiency is
> judged against the *designed* contract, with build-status noted.

### B1 — n8n compat plugin (poller-based) — *the flagship (issue #35)*
Poll n8n's execution API and translate workflow runs into traces server-side.
- **Primitives:** `ingest` (write translated spans), `secrets` (n8n API creds), `jobs` (the poll loop), `write`.
- **Suffices?** **No — three of four are absent, and the key one is under-specified.** The `ingest` capability is an enum value the manifest schema itself marks "Tier-3 work"; there is **no endpoint- or cold-path-normalizer registration mechanism** designed in detail. `jobs` and `secrets` are enum-only. `langfuse-compat` — the sibling that would prove this shape — is an empty scaffold.
- **Fit:** 🟡→🟠 — the *thesis* is sound (this is exactly what a compat plugin should be, and it validates why Langfuse can't ship n8n), but the ingest-registration contract must be specified before it's buildable. This story is the technical precondition of issue #35.

### B2 — Cost-budget alerts plugin
Subscribe to span ingests, accumulate rolling cost per project, fire a webhook over budget.
- **Primitives:** `events` (trigger), `kv` (running totals), `jobs` (windowing/flush), `secrets` (webhook URL), `surface` (config tab).
- **Suffices?** Designed shape suffices *conceptually*, but the `events` primitive has **no subscribe surface** even in design — only an internal publish stage. `kv/jobs/secrets` absent.
- **Fit:** 🟡 — designed-not-built, with a genuine contract gap: `events` needs a plugin-facing subscribe/consume API (issue #14 + #36).

### B3 — LLM-judge eval plugin (issue #25)
On each new generation, run an LLM judge and write a score.
- **Primitives:** `events` (new-generation trigger), `query` (fetch the span), `secrets` (judge API key), `jobs` (async run), `write` (the score).
- **Suffices?** `query` 🟢 and score-**write** exists in the kernel (`WriteScore`, scope `scores:write`) — but **not in the SDK client**, so an author hand-rolls HTTP. `events/jobs/secrets` absent. The score model genuinely fits (subject_type=span, categorical/numeric) — that half is real.
- **Fit:** 🟡 — the *data contract* (scores) is 🟢; the *author ergonomics* (no SDK write, no trigger, no secret) are designed-not-built. Add score-write to the SDK and this becomes mostly a wiring exercise.

### B4 — Configurable dashboards plugin
Render charts over the Query API; let users save layouts.
- **Primitives:** `query` (aggregations), `kv` (saved layouts), `surface` (the tab).
- **Suffices?** `query` 🟢 (aggregations, groupBy, date_bin are real) and `surface` 🟢 (frontend). **`kv` is absent** — and the frontend rule forbids `localStorage` in SDK components, so saved layouts have *nowhere to live* today.
- **Fit:** 🟡 — two of three primitives are 🟢; the whole story is blocked only on a small `kv`. Cheapest B-set unlock.

### B5 — Prompt-management plugin
Versioned prompts with references/composability, CRUD in a tab, served to SDKs.
- **Primitives:** plugin-owned structured entities (`write`/`kv`?), `query`, `surface`.
- **Suffices?** **No.** Prompts are *not* in the canonical model — they are versioned, referenceable, queryable **plugin-owned relational entities**. `write` is canonical-scores-only; `kv` is opaque blobs (no versioning, no relational query). Neither expresses "list prompt versions where … ordered by …".
- **Fit:** 🟠 — needs contract evolution. Together with B7/B8 this is the pressure point on the seven-primitive thesis (see verdict).

### B6 — Plugin calls an external provider and needs an API key
Any plugin that calls an LLM/provider needs a stored credential, never exposed to the browser.
- **Primitives:** `secrets`.
- **Suffices?** Designed intent is right (store server-side, inject into the plugin backend, never to frontend), but there is **no secrets provider, no manifest secret-ref, no injection path** — all absent.
- **Fit:** 🟡 — designed-not-built; small and self-contained once a provider + manifest field exist (ties to A5).

### B7 — Plugin persists its own relational entities (datasets, experiment runs)
A plugin owns domain tables the kernel has never heard of, and must query/join them.
- **Primitives:** `write`, `kv`, `query` — *as generalized to plugin-owned data*.
- **Suffices?** **No — this is the thesis's hard case.** `query` targets only spans/traces/scores; `write` only scores; `kv` is key→blob with no query/index/join. There is **no primitive for plugin-owned structured, queryable storage**, and the dogfood rule forbids a plugin opening its own DB. A backend feature-plugin literally cannot persist-and-query its own domain.
- **Fit:** 🔴 — resists under the current seven. This is where an eighth primitive is warranted (see verdict).

### B8 — Nightly aggregation job writing derived rows
A scheduled rollup reads traces, computes per-day aggregates, and stores them for fast dashboards.
- **Primitives:** `jobs` (schedule), `query` (read), `write`/`kv` (store derived rows).
- **Suffices?** `query` 🟢 for the read. `jobs` absent (no scheduler, and no plugin backend to run in). The *derived-storage* half hits the same wall as B7 — rollups aren't scores and don't belong in `kv`.
- **Fit:** 🟠 — `jobs` is designed-not-built; the derived-data sink is the B7 gap. Two missing pieces, one of them contract-level.

### B9 — Settings tab with a schema-form config UI
A plugin declares a settings schema and renders a generated form; values persist.
- **Primitives:** `surface`, `kv` (persist the values).
- **Suffices?** `surface` 🟢 and the manifest **`settingsSchema`** field is real (`llmobs-plugin.schema.json`), with `packages/schema-form` as the designed renderer. **Value persistence needs `kv`, which is absent** — the manifest declares the schema but nothing stores the answers.
- **Fit:** 🟡 — the declaration half is real; persistence is designed-not-built (again just `kv`).

### B10 — Real-time enrich/annotate a specific span kind
On each `retrieval` span, attach a label/annotation derived server-side.
- **Primitives:** `events` (filtered trigger), `write` (the annotation), `query` (context).
- **Suffices?** The annotation maps cleanly onto the **score model** (subject_type=span, categorical) — that's 🟢 by design, a nice fit. But you **cannot mutate a persisted span** via any plugin path (only scores), and the `events` trigger to fire on it is absent.
- **Fit:** 🟡 — annotation-as-score is designed-🟢; the event trigger is designed-not-built. Note the deliberate constraint: plugins annotate *alongside* spans (scores), never *edit* them — consistent with raw-preservation.

---

## Classification table — Set A

| # | Failure mode | Blast radius | Acked data lost? | Fit |
|---|---|---|:--:|:--:|
| A1 | Postgres disk full mid-ingest | ~~one node, silent~~ → **shed 503, /readyz not-ready** | ~~YES~~ → **NO** ✅ | ~~🟠~~ → 🟢 |
| A2 | Rolling upgrade / migration skew | none if additive; old replica if destructive | NO | 🟡 |
| A3 | ClickHouse down (scale) | N/A — scale profile absent | N/A | 🟡 |
| A4 | Shared plugin runtime OOM | one UI tab (backends don't exist) | NO | 🟡 |
| A5 | Manifest → missing secret | N/A — no secrets/manifest-ref | NO | 🟠 |
| A6 | SIGTERM with full buffer | ~~one pod, every deploy~~ → **queue drained; bounded floor counted** | ~~YES~~ → **NO** ✅ | ~~🟠~~ → 🟢 |
| A7 | Airgap missing asset | N/A — no airgap tooling | NO | 🟡 |
| A8 | Event bus wiped / subscriber down | event-derived side effects only | NO (telemetry safe) | 🟡 |
| A9 | Erasure vs. re-delivery race | ~~erased PII resurrected~~ → **tombstone refuses re-delivery** | NO — ~~inverse: over-retention~~ **now durably erased** ✅ | ~~🟠~~ → 🟢 |
| A10 | GitOps drift / reconcile | N/A — no supervisor | NO | 🟡 |

> **Update — closed by `feature/kernel-durability-triad` (the Durability Triad arc).**
> A1, A6, and A9 have flipped:
> - **A6 (G1):** shutdown now drains the ingest queue before exit; a rolling deploy
>   loses nothing. The only residual is a forced-kill past the drain deadline, which
>   is **counted** (`llmobs_ingest_queue_dropped_on_shutdown_total`), not silent.
>   Blast radius → a bounded, observable floor; acked data lost → **NO** on the
>   routine path.
> - **A1 (G2):** persist failure feeds `/readyz` (not-ready) and the OTLP receivers
>   shed a retryable `503`+`Retry-After` / `UNAVAILABLE` instead of false-acking into
>   a queue that can't drain. Blast radius → no more silent green readiness; acked
>   data lost → **NO** (clients retry into the idempotent merge).
> - **A9 (G3):** erasure writes a per-id, TTL-bounded suppression tombstone the
>   persist path enforces, so a re-delivery cannot resurrect erased PII. The mirror
>   risk (over-retention) is closed; erasure is now durable across redelivery.
>
> The remaining ack-before-durable *crash* window (SIGKILL/OOM) is the lite
> profile's honest, bounded floor; the scale profile's durable spool closes it and
> is out of scope for this arc.

Originally two places lost acknowledged data (A1, A6) and one over-retained (A9);
all three are now closed on the routine paths — the ack is a promise the lite
profile keeps up to a bounded, counted crash floor.

## Classification table — Set B

| # | Story | Primitives exercised | Sufficient (designed)? | Fit |
|---|---|---|:--:|:--:|
| B1 | n8n compat poller | ingest, secrets, jobs, write | No — ingest under-specified | 🟡→🟠 |
| B2 | Cost-budget alerts | events, kv, jobs, secrets, surface | Partial — events needs subscribe API | 🟡 |
| B3 | LLM-judge eval | events, query, secrets, jobs, write | Data-contract yes; ergonomics no | 🟡 |
| B4 | Configurable dashboards | query, kv, surface | Yes once kv exists | 🟡 |
| B5 | Prompt management | (plugin-owned entities), query, surface | **No** | 🟠 |
| B6 | External-provider key | secrets | Yes (design); absent | 🟡 |
| B7 | Plugin-owned relational entities | write, kv, query (generalized) | **No — resists** | 🔴 |
| B8 | Nightly aggregation job | jobs, query, write/kv | No — jobs + derived-sink gap | 🟠 |
| B9 | Settings schema-form tab | surface, kv | Declaration yes; persistence no | 🟡 |
| B10 | Real-time span annotation | events, write, query | Annotation-as-score yes; trigger no | 🟡 |

**`query` and `surface` (frontend) carry every story that stays inside them (B4, B10-read).
The recurring blockers are, in order: `kv` (B4/B9), `events`-subscribe (B2/B3/B10),
`jobs` (B1/B2/B3/B8), `secrets` (B1/B3/B6), and — uniquely un-papered-over — plugin-owned
structured storage (B5/B7/B8).**

---

## Top-3 cheapest changes

**Set A (deployment):**
1. **Drain the ingest queue on shutdown** — workers finish the channel before exiting; `Stop()` flushes with a bounded deadline (`receiver.go:105-116`). Closes A6 (the every-deploy acked-loss) in real code, tiny diff.
2. **Make persist-health a readiness signal + backpressure** — on repeated `PersistSpan` failure (disk full, pool exhausted) flip `/readyz` to 503 and return 503 at ingest so clients retry into the idempotent merge instead of getting a false 200 (A1). Reuses the existing merge idempotency as the safety net.
3. **Erasure suppression tombstone** — a small `erased_keys` table the persist stage consults so a re-delivered erased span is refused/re-erased (A9). One table, one predicate; turns erasure from "point-in-time delete" into "durably gone."

**Set B (plugin platform):**
1. **Ship `kv` + expose score-`write` in the SDK client** — the two smallest primitives, both with the data model already implied. Unblocks B4, B9, and the ergonomic half of B3/B10 immediately.
2. **Define the `events` subscribe/consume contract** (issue #14/#36) — even a cursor-based "read span.ingested since X" is enough to unblock B2/B3/B10 triggers; the seam (`EventBus`/`publishStage`) is already correctly placed.
3. **Specify the `ingest` endpoint-registration contract for compat plugins** (issue #35) — the cold-path own-endpoint + normalizer registration the n8n flagship needs; specify before building so the manifest and double-token model cover it.

---

## Two pull-forward judgments

**Single deployment-hardening item most worth doing before Tier-3:**
**Drain-on-shutdown + persist-failure backpressure (A6, then A1).** It is the *only*
place acknowledged data is lost on the **routine** path — every rolling deploy sheds a
bounded batch of acked spans today — it is real code with a small, well-scoped diff, and
it makes the existing idempotent-merge backstop actually reachable (clients retry a 503;
they never retry a false 200). Everything else in Set A is either absent-by-design (safe
to defer with the honest flag) or contract work that can wait; this one is losing real
data on ordinary operations right now.

**Single plugin-platform item most worth doing before Tier-3:**
**A plugin-owned structured-storage primitive (resolves B5/B7/B8).** It is the one gap the
other six primitives cannot paper over, and it is not hypothetical: the first Tier-3
first-party plugins — evals (datasets/experiment runs) and prompt-management (versioned
prompts) — *are exactly* the B5/B7 shape. Without it, those plugins will reach for their
own database and break the dogfood rule (D2/D3) on day one. Deciding this before Tier-3
is the difference between the plugin ecosystem the thesis promises and a set of plugins
that quietly bypass the platform.

---

## Verdict — does the seven-primitive thesis survive Set B?

**Mostly — with one structural exception that Set B makes unavoidable.** For plugins that
*consume* observability data — dashboards, alerters, LLM-judges, annotators, settings UIs
(B2, B3, B4, B6, B9, B10) — the seven primitives are the right decomposition: `query` +
`surface` + score-`write` + `kv` + `events` + `jobs` + `secrets` cover every one of them
once built, and the capabilities stay honest nouns-about-data. The thesis holds cleanly
there, and the seam placement (event bus after persist, scores as annotations alongside
immutable spans, frontend via Module Federation) is sound.

It breaks on **plugins that own their own domain data** (B5 prompt-management, B7
datasets/experiments, B8 derived rollups). `kv` is opaque key→blob and cannot express
versioned, relational, *queryable* entities; `write`/`query` are canonical-only
(spans/traces/scores); and the dogfood rule rightly forbids a plugin opening its own DB.
There is simply no capability for "a plugin's own structured, queryable, tenant-scoped
tables" — and the very first Tier-3 first-party plugins need exactly that.

**An eighth primitive is needed. Name it `store`** — a namespaced, plugin-owned collection
primitive: the plugin declares typed collections in its manifest (like `settingsSchema`,
but for data), and gets tenant-scoped `store.put` / `store.query` behind the gateway with
the same permission-intersection and provenance discipline as the telemetry path — **never
raw DB access.** It keeps the invariant intact ("capabilities are nouns about data":
`store` is a noun) and is the minimal addition that lets feature-plugins own their domain
without touching infrastructure. (The alternative — generalizing `write`+`query` from
"canonical model" to "canonical + plugin-declared collections" — reaches the same place;
but that stretches two primitives whose current contract is explicitly canonical-only, so
a distinct `store` primitive is the cleaner, more honest evolution.) Seven stands for the
consumer surface; the platform needs an eighth to host producers of their own data.

---

## Set-B re-color after Arc H (Tier-3)

Re-evaluated against `develop` post-H1–H8. Vocabulary as before: 🟢 first-class /
🟡 works-but-not-first-class or blocked-on-DX / 🟠 needs-contract-evolution /
🔴 resists. Written adversarially — looking for what is still short.

| # | Story | Was | Now | What moved it — and what's still short |
|---|-------|:--:|:--:|----------------------------------------|
| B1 | n8n compat poller | 🟡→🟠 | 🟢\* | `ingest` cold-path (H7a, service-token-only) + `jobs` poller (H6b) + `secrets` (H4b) — and **langfuse-compat proves the exact shape end-to-end in CI**. \*The *platform* demonstrably supports it; n8n-the-plugin is a poller someone writes, not a platform gap. |
| B2 | Cost-budget alerts | 🟡 | 🟡 | `events` subscribe (H6a) + `jobs` + `secrets` + `kv` all real → buildable as a **backend** plugin. Stays yellow: the frontend config tab's persistence needs frontend-direct `kv`, which is deferred. |
| B3 | LLM-judge eval | 🟡 | 🟢 | `events` (subscribe) + `query` + `secrets` (judge key) + `jobs` + **score-write in the SDK** (H4b) — the read→judge→write loop is real. (Topic coverage: `span.ingested` today; `trace.completed` additive.) |
| B4 | Configurable dashboards | 🟡 | 🟡 | `query` + `kv` are real, but `kv` is backend-double-token only. A **pure-frontend** dashboard can't persist layouts without a backend — the frontend-direct path is deferred. |
| B5 | Prompt management | 🟠 | 🟢 | **`store` (H5)** gives versioned, queryable, plugin-owned prompt entities — the data model gap is closed. (Frontend-direct CRUD UX is the same deferred frontend path.) |
| B6 | External-provider key | 🟡 | 🟢 | `secrets` (H4b), with the tested "never returned by any read" prove-the-negative. First-class. |
| B7 | Plugin-owned relational entities | 🔴 | 🟢 | **The headline flip.** `store` (H5) — the eighth primitive — provides plugin-owned, tenant-scoped, queryable collections, with the cross-tenant isolation prove-the-negative. (No joins by design, R4 — a plugin denormalizes.) |
| B8 | Nightly aggregation job | 🟠 | 🟢 | `jobs` (H6b) schedule + `query` read + **`store` as the derived sink** (H5) — the derived-storage gap that made this 🟠 is closed. The job runs under a bounded system identity (H6b pin 1). |
| B9 | Settings schema-form tab | 🟡 | 🟡 | `settingsSchema` declared + `kv` real, but the **`packages/schema-form` renderer is still designed-not-built**, and settings persistence from a frontend tab needs the deferred frontend-direct path. |
| B10 | Real-time span annotation | 🟡 | 🟢 | `events` subscribe (H6a) is the trigger (was designed-not-built) + annotation-as-score via score-write (H4b). (Filters `span.ingested` client-side; annotates alongside spans, never mutates — by design.) |

### What moved, and what deliberately didn't

- **The eighth primitive resolved the hardest gaps.** `store` (H5) flipped the only
  🔴 (B7) to 🟢 and resolved B5/B8 — plugin-owned structured data was the one thing
  the other primitives could not paper over, and it's now real and isolation-proven.
- **Backend plugins are first-class; six stories are 🟢.** B3, B6, B7, B8, B10 (and
  B1's substrate) are buildable end-to-end today, each on a tested primitive.
- **Three stories stayed 🟡 — B2, B4, B9 — for one shared reason.** All three are
  *pure-frontend* plugins that need to persist config/settings, and Arc H
  deliberately deferred the **frontend-direct plugin identity** (a shell-minted,
  per-plugin assertion) — so `kv`/`store`/`settings` are backend-double-token only
  (the honest H4a deferral). B9 additionally waits on the `schema-form` renderer.
  These yellows are not oversights; they are the **post-Tier-3 roadmap**, and the
  DX audit names the two unlocks (frontend-direct identity + `make dev`
  hot-reload).
- **No 🔴 remain, and no 🟢 was granted a story an author can't actually build.**
  The distinction B1 draws — platform-enables vs plugin-is-written — is kept
  explicit rather than counted as a win.

## Set-B re-color after Arc J — closing the three yellows

Arc J set out to flip exactly the three yellows Arc H named (B2, B4, B9), all held by
the same missing pieces: a *pure-frontend* plugin could not act with a confined
identity, could not persist settings, and could not be developed comfortably. Three
PRs closed them in dependency order.

| # | Story | Was | Now | What moved it — and what's still short |
|---|-------|:--:|:--:|----------------------------------------|
| B2 | Cost-budget alerts | 🟡 | 🟢 | The backend alert engine was already buildable (`events` + `jobs` + `secrets` from Arc H); the **frontend config tab** — thresholds, the alert channel key — is now first-class: it persists through the **settings store (J2)** under the **frontend token (J1)**, with the channel key a `writeOnly` secret that is never rendered back. Both halves are real. |
| B4 | Configurable dashboards | 🟡 | 🟢\* | A pure-frontend dashboard now persists its layout through the settings store (J2) with no backend — the exact gap that held it. \*Honest caveat: settings are **project-shared and admin-write** in the coarse model, so a *shared/team* dashboard is 🟢 today; a viewer saving their **own** per-user layout needs per-user settings (a named future dimension, ADR-0024), not a new primitive. |
| B9 | Settings schema-form tab | 🟡 | 🟢 | **The headline flip.** `packages/schema-form` (J2) renders the manifest's `settingsSchema` as a settings tab, validated client + kernel side; values persist via the frontend-token-scoped settings store; `writeOnly` fields are encrypted and never returned. Declaration → rendering → persistence → secrets, all real. Dogfooded by `plugins/tracing`. |

### What moved in Arc J, and the new yellows

- **The two Arc-H unlocks landed.** The H8 re-color named the post-Tier-3 roadmap as
  *frontend-direct identity* + *`make dev` hot-reload*. J1 built the first (as
  least-privilege-by-default, honestly **not** a hostile-frontend boundary — see
  [trust-model.md](../plugin-authors/trust-model.md)); J3 built the second (kernel +
  shell + plugin, frontend HMR via a dev-remote override) and added the missing **SDK
  test utilities**. The DX gaps the audit called binding are closed.
- **All ten Set-B stories are now 🟢** (B1's substrate + B2–B10). No 🟡, no 🔴 remain.
- **The new yellows are honestly named, not hidden:**
  - **Frontend origin isolation** (🟡, security posture) — the frontend token is
    least-privilege-by-default, not a boundary; a hostile same-origin frontend can
    bypass it. A real boundary (sandboxed cross-origin iframe + postMessage) is
    specified and triggered by the first *untrusted* third-party frontend plugin
    (ADR-0004 amendment / ADR-0023 deferred). Until then: untrusted logic → a backend.
  - **Per-user settings** (🟡, feature dimension) — settings are project-shared +
    admin-write today; per-user config (B4's per-viewer layout) is a future dimension.
  - **CLI breadth** (🟡, DX) — `init/dev/apply/render/bundle/backup` remain skeletons;
    `plugin create` is the one real verb. And `make dev` wires **one** first-party
    plugin end-to-end; the mechanism is general, the orchestration lists one.
  - **Coarse RBAC** (🟡, pre-existing) — admin vs viewer only; finer roles are the
    standing #21 seam that both J2's write-authority gate and the supervisor lean on.
