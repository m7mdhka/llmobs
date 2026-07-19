# Architecture Decision Records

Numbered, immutable-once-accepted records of the architectural decisions behind
LLMObs. Use the `new-adr` skill to add one. An ADR is required in the same PR
for: a new hot-path dependency, a new public API, a storage schema change, a new
top-level directory, or breaking a published contract.

## Index

ADR-0001..0015 backfill the locked design decisions (the "D-table") so the
decision history exists from the start:

| ADR | Decision |
|---|---|
| ADR-0001 | Microkernel architecture (D1) |
| ADR-0002 | Dogfood rule — first-party plugins use only the public API (D2) |
| ADR-0003 | OTLP-canonical ingestion; normalizers vs. compat plugins (D3) |
| ADR-0004 | Plugins are containers + manifest; MF 2.0 frontends (D4). **Amended (J1, see [ADR-0023](0023-plugin-protocol.md) "Frontend token"):** MF frontends share the shell's origin, so a frontend-only plugin is *trusted-at-install*; the frontend token is least-privilege-by-default, not a boundary. Origin isolation (cross-origin sandboxed iframe + postMessage) is the deferred real boundary. **Amended (N1, see [ADR-0030](0030-framework-neutral-frontend-contract.md)):** a plugin frontend exports a framework-neutral `mount(element, context) → unmount` contract; React is one binding (`@llmobs/plugin-sdk/react`), not the binding. MF stays the loader; what a remote exports changes from a React component to `mount`/`unmount`. |
| ADR-0005 | Double-token auth; permission intersection (D5) |
| ADR-0006 | Declarative supervisor, pluggable executors (D6) |
| ADR-0007 | Two deployment profiles: lite and scale (D7) |
| ADR-0008 | Seven SDK primitives; capabilities are data nouns (D8) |
| ADR-0009 | Typed query DSL, compiled per adapter — no raw SQL (D9) |
| ADR-0010 | Ingestion middleware chain; redaction before persistence (D10) |
| ADR-0011 | K8s-style API versioning; additive-only within a major (D11) |
| ADR-0012 | GitHub-native plugin distribution; namespaced IDs (D12) |
| ADR-0013 | Performance targets for lite and scale (D13) |
| ADR-0014 | Governance: DCO, Apache-2.0, air-gap as first-class (D14) |
| ADR-0015 | Rename stays cheap — brand in one module per language (D15) |

ADR-0001..0015 are backfilled during bootstrap (see the design doc's bootstrap
order). New decisions continue the numbering:

| ADR | Decision |
|---|---|
| [ADR-0016](0016-canonical-data-model-v1alpha1.md) | Canonical data model `v1alpha1` (master; LM-1..LM-12) |
| [ADR-0017](0017-score-model.md) | Score model (LM-3 + LM-8) |
| [ADR-0018](0018-span-taxonomy-and-payload-shapes.md) | Span taxonomy and payload shapes (LM-1 + LM-2) |
| [ADR-0019](0019-query-dsl.md) | Query DSL (QD-1..QD-10) |
| [ADR-0020](0020-indexed-attribute-keys.md) | Indexed attribute keys — per-project custom dimensions (audit) |
| [ADR-0021](0021-explicit-clear-sentinel.md) | Explicit-clear sentinel — one merge algorithm survives (audit) |
| [ADR-0022](0022-time-authority-policy.md) | Time authority policy — producer vs receive time (audit) |
| [ADR-0023](0023-plugin-protocol.md) | Plugin protocol (Tier-3 backend): handshake, tokens, R1–R4 (Arc H) |
| [ADR-0024](0024-plugin-settings-schemaform.md) | Plugin settings store + SchemaForm: kv-backed, frontend-token-scoped, `writeOnly` secrets never returned (Arc J / J2) |
| [ADR-0025](0025-prelaunch-design-rules.md) | Pre-launch design-rules from the Langfuse merged-PR mine: cost-derivation, outbound-fetch + egress watchdog, token-revocation freshness, limiter fail-open/closed, agent-tool gating, convergence-point, self-hosting (Arc K / K2) |
| [ADR-0030](0030-framework-neutral-frontend-contract.md) | Framework-neutral plugin frontend contract — `mount(element, context) → unmount`; React becomes one binding (`@llmobs/plugin-sdk/react`); amends ADR-0004 (Arc N / N1) |
| [ADR-0031](0031-rbac-user-org-role-foundation.md) | Real auth foundation — users, orgs, per-org memberships, resource:verb RBAC (owner/admin/member/viewer); role scopes share the plugin-grant currency; server-derived, no plan gating; amends ADR-0005 (Arc O / O1) |
| [ADR-0036](0036-event-bus-poison-message-deadletter.md) | Event-bus poison-message dead-letter — `events.fail(topic,id,reason)` as the permanent-vs-transient seam; head-only, single-event, no Store change; a poison can no longer head-of-line-block or trigger the bulk-drop of good events (#110) |
| [ADR-0037](0037-blobs-primitive.md) | First-class `blobs` primitive — `blob.Store` seam + one tenant-scoped, fs-safe (#92), injective (#101) key derivation as the isolation convergence point; local adapter (lite) landed; gateway/SDK/S3/signed-URL/GC are the tracked build follow-on (#120) |
