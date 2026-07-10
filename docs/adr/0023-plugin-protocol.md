# ADR-0023: Plugin protocol (Tier-3 backend)

- **Status:** Accepted (implementation in progress — Arc H)
- **Date:** 2026-07-10
- **Deciders:** m7mdhka (Tier-3 arc, flexibility-audit Wave-3 remediation)
- **Relates to:** ADR-0002 (dogfood rule), ADR-0004 (plugins are containers +
  manifest), ADR-0005 (double-token auth), ADR-0006 (supervisor + executors),
  ADR-0008 (SDK primitives). Closes the Wave-3 Set-B gaps (`docs/research/
  flexibility-audit-wave3-stories.md`): the seven primitives were enum-only, the
  double-token intersection was named-not-computed, and there was no primitive for
  plugin-owned structured data.

## Context

After the Wave-3 audit, a plugin could ship a frontend tab and read via `query`;
nothing else. `events/jobs/kv/secrets/store/ingest` were manifest enum strings
with no backing code, `pkg/pluginproto` was an empty `.gitkeep`, the permission
intersection was stubbed to empty (`server_gen.go`) with a fixed-full-admin
shortcut (`query/server.go:55`), and there was no way for a plugin to own its own
structured, queryable data — the one gap the other primitives could not paper over
(Set-B B5/B7/B8), which the audit said needed an **eighth primitive, `store`**.

This ADR fixes the **protocol** (this PR, H1) and records four architecture-level
rulings that the rest of Arc H (H2–H8) implements without relitigating.

## Decision — the protocol (H1)

A **plugin backend** is an ordinary HTTP service the operator runs, registered by
URL (`spec.backend`), that the kernel discovers, handshakes with, supervises, and
proxies user traffic to. The normative contract is `api/plugin/v1alpha1`; the
public Go types + stdlib crypto helpers are `kernel/pkg/pluginproto` (the only
kernel package a plugin backend imports — the dogfood rule).

- **Handshake** (`GET /plugin/v1/info`): id-match, protocol-compatibility, and
  capability-subset checks gate token issuance. The manifest, not the plugin, is
  authoritative for capabilities.
- **Two-signal health** (`GET /plugin/v1/health`): `live`/`ready` plus an optional
  **functional watermark** — process-alive ≠ work-happening (the G2 lesson). A
  ready plugin with a watermark stale past `spec.backend.watermarkBudget` is
  `degraded`; a busy long-running job that keeps advancing its watermark is not
  (the B3 lesson).
- **Two tokens, compact + Ed25519-signed** (`v1.<base64url(claims)>.<base64url(
  sig)>`, no JWT dependency): a **service token** (which plugin, what it may do;
  short-TTL + refresh) and a per-request **identity assertion** (who the user is,
  what they may see; audience-bound to `plugin:{id}`, short-TTL). Ed25519 is
  asymmetric so a plugin verifies a kernel-minted assertion without a shared
  secret.
- **Gateway proxy** (`/api/plugins/{id}/*`): only `running` plugins are proxied;
  the cookie is stripped and the assertion injected — the kernel remains the single
  place a cookie becomes an identity, now code-defended because a plugin backend
  path exists.

## Ruled decisions (R1–R4) — do not relitigate downstream

- **R1 — `store` physical model.** Plugin-owned structured data lives in
  **plugin-namespaced Postgres schemas in the shared database** (lite), behind a
  `PluginStore` interface written so a dedicated-database backend slots in for
  scale — mirroring the telemetry storage-adapter seam. The kernel owns the schema
  lifecycle (create/migrate/drop per plugin); the plugin never receives a
  connection string. (H5)
- **R2 — Executor.** Implement the **external-URL executor only** this arc. The
  plugin backend is a service the operator runs however they like; the kernel
  handshakes and supervises what's reachable. Compose/operator/GitOps executors
  are deferred (issues). No Docker-socket access — the deployability invariant. (H2)
- **R3 — Computed double-token intersection.** The intersection is **really
  computed and enforced**: effective access = service-token scopes ∩
  user-assertion scopes ∩ project scope, at the Query API and every
  plugin-reachable surface. The empty-scope stubs and fixed-full-admin shortcut are
  removed. JWKS distribution + key rotation are a deferred hardening pass (issue).
  (H3)
- **R4 — `store` query surface.** A deliberately smaller cousin of the telemetry
  DSL: filter/order/paginate over manifest-declared indexed fields only. **No
  aggregations in v1, no joins ever.** A noun-about-data primitive, not a second
  query engine. (H5)

## Lite-profile property — in-memory signing key (H2)

The kernel's Ed25519 signing key is generated **in memory at boot** and not
persisted (persisted/shared keys are the deferred JWKS/rotation pass). Therefore a
**kernel restart rotates the key and invalidates every live service token**. This
is by design harmless because tokens are short-TTL, but it imposes one rule on the
supervisor state machine:

> A service token that no longer verifies after a kernel restart is a **normal
> re-handshake trigger, NOT a plugin fault**. It must not count toward the
> exponential-backoff-to-`disabled` cap — otherwise a routine kernel restart would
> march every healthy plugin toward auto-disable.

The supervisor satisfies this structurally: "no valid token" routes to the
handshake path, and only a handshake/health *failure* increments the fault
counter. After a restart the supervisor's in-memory state is empty, so it
re-handshakes every plugin and returns the healthy ones to `running` with zero
faults (proved by `TestKernelRestartReHandshakeIsNotAFault`). This is a known,
accepted lite-profile property; the scale profile's persisted/shared key removes
it (deferred).

## Scope vocabulary — canonical is the fine-grained noun set (H3)

The double-token intersection needs one currency, and two vocabularies exist:
coarse api-key/session **verbs** (`query`, `query:payloads`, `scores:write`,
`delete`, `ingest`) and fine-grained manifest-permission **nouns**
(`traces:read.metadata`, `traces:read.payloads`, …). **The fine-grained noun
vocabulary is canonical; the coarse verbs translate UP into it** (`perm.
ExpandCoarse`). The direction is load-bearing: "capabilities are nouns about data,
never verbs about features" is a founding invariant, and translating toward the
richer vocabulary is lossless and additively extensible — a new permission
(`scores:read` distinct from write, a future `store:read.collection_x`) just gets
named. Collapsing down to the five verbs would be a one-way door capping the
permission model at today's operations. A plugin's service token carries data
permissions (nouns) plus prefixed **capability markers** (`cap:query`) — two axes,
never conflated: capabilities gate the endpoint, permissions gate the data.

## Inert generated stubs (H3, documented not deleted)

The generated `queryapi.server_gen.go` declares `ServiceTokenScopes`/
`UserAssertionScopes` security-context values but leaves them empty. They are
**deliberately ignored**: the real intersection is computed in `query.auth()` from
the verified token + assertion headers. We do **not** hand-edit generated code
(the rule) nor regenerate merely to delete cosmetically-dead stubs (churn). A
one-line note at the `auth()` computation site explains *why* they're ignored so a
future reader does not wire them up thinking they are authoritative. Inert-and-
documented, not inert-and-mysterious.

## Consequences

- The manifest gains an additive `spec.backend` (url, healthPath, infoPath,
  watermarkBudget); Tier-2 plugins ignore it.
- `pkg/pluginproto` is now real and semver-sacred for plugin authors; the JSON
  Schemas in `api/plugin/v1alpha1` are the contract, the Go types track them.
- The eighth primitive `store` is admitted (R1/R4), extending ADR-0008's "seven"
  to eight — the audit's recommended resolution to the B5/B7/B8 gap.

## Deferred (issues, referenced)

Compose/operator/GitOps executors; NATS scale event-bus backend; JWKS + key
rotation; the browser SSE bridge; KMS secret backend; dedicated-DB `store` backend
for scale. Each is tracked so the deferral is explicit, not silent.
