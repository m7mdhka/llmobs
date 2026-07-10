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
