# ADR-0025: Pre-launch design-rules (from the Langfuse merged-PR mine)

- **Status:** Accepted (Arc K / K2)
- **Date:** 2026-07-11
- **Deciders:** m7mdhka (Arc K — pre-launch correctness pass)
- **Relates to:** ADR-0023 (plugin protocol), ADR-0024 (settings), ADR-0016/0021
  (canonical model / merge), the Query DSL (ADR-0019). Source evidence: the Langfuse
  merged-PR mine — 400 merged PRs (`langfuse/langfuse` #14317–#14991) read for the bugs
  and hardenings a mature incumbent actually shipped. Every rule below cites the public
  PR/issue number it was derived from, checkable in that tracker; the mining method is
  recorded in `docs/positioning.md` (Evidence appendix).

## Context

The Langfuse merged-PR mine read the fixes Langfuse actually shipped, judged against
our architecture. It found **~zero launch-gating bugs on our current surfaces** — we
are immune by design to whole classes of their bugs (F3 provider-verbatim cost,
frozen-field entity-splitting, erasure-tombstone replay, DSL type-guards, id-keyed
merge). K1 shipped the handful of real correctness fixes, each fixture-proven.

What remained was a coherent set of **design-rules that gate code we have not
written yet** — cost derivation, outbound fetch, token revocation, RBAC. Pinning
these now, before the code exists, is the cheapest correctness the project buys: a
future contributor inherits the rule by construction instead of re-discovering the
Langfuse bug. This ADR is the normative home for those rules; the sections below say
where each is also pinned in the spec/docs a builder actually reads.

Each rule cites the Langfuse PR(s) that are the evidence, not the authority — the
authority is our own invariants; the PRs show the failure mode is real and shipped.

## Decision — the rules

### R1 — Cost derivation MUST NOT double-count or fabricate (→ `06-usage-cost.md` §7)

The enrich/derivation stage is a no-op today (`pipeline/stages.go`), so these are
free to get right. When it is built it MUST honor:

- **(a) Aggregate spans duplicate leaf usage.** An `agent_step`/`invoke_agent`
  span often carries the same usage as its child model-call span. Trace-level cost
  aggregation MUST NOT sum both — that double-counts the trace's cost. (Evidence:
  Langfuse #14808 zeroes usage on AI-SDK agent spans for exactly this.)
- **(b) `input` may be inclusive of cache.** F3 forbids assuming either way, so the
  synthesized `total` and any derived cost MUST NOT sum `input + cache_read` as
  disjoint buckets — that is the mirror of Langfuse's subtraction bug (#14902/#14945).
  Synthesize `total` from `input + output` only; treat cache/reasoning as additive
  detail, never re-summed.
- **(c) A `model` without provided usage MUST NOT trigger phantom estimation.** Usage
  derivation is permitted only when a model resolves AND `status.code != error`
  (§3.2) — but a wrapper/agent span with a `model` and no real usage must not be
  tokenized into estimated usage and priced, or the trace grows phantom cost
  (#14945). Gate estimation on the span actually being a leaf generation with no
  child carrying the usage.

### R2 — Outbound fetch is fail-closed, timed-out, SSRF-guarded, and watchdogged (→ plugin-author security docs)

No kernel surface makes a user-influenced outbound call today. Plugins call external
LLM providers (the `secrets` capability, B6); future kernel evals/webhooks will. For
**any** such surface:

- **Fail-closed at the convergence point** — enforce the guard at the one seam every
  scheduling path funnels through, never per-entry-point (per-entry guards drift; a
  new path forgets one). (Evidence: Langfuse #14835 — two new eval paths bypassed
  per-entry guards in one week.)
- **Propagate the caller's abort/timeout** to the underlying fetch and enforce a hard
  outbound timeout; **enumerate providers, don't allowlist** (a hand-maintained
  provider allowlist drifts and leaves some egress unbounded). (Evidence: #14876
  abort-signal not forwarded → an 18-minute call; #14466/#14455 an allowlist left
  Anthropic/Azure/Bedrock egress un-timed-out.)
- **Block internal/link-local hosts** (SSRF) — an outbound call must not reach
  `169.254.0.0/16`, `127.0.0.0/8`, `10/8`, `metadata.google.internal`, etc.
- **Watchdog the shared lite runtime (enforcement teeth).** In the lite profile,
  plugins share **one host process** (`runtimes/shared/`). One plugin's hung outbound
  call can starve *every* plugin in that process — a mandatory per-call timeout is
  necessary but not sufficient. The supervisor/host MUST be able to **detect a plugin
  pinning the shared runtime and kill/restart it**, reusing the H2 two-signal health +
  `degraded` machinery (a plugin whose functional watermark stalls while a call hangs
  is `degraded`, and dev-restart-not-a-fault already covers the restart). This is
  **rule-not-code** (the outbound surface isn't built) but the timeout **and** the
  watchdog are **non-optional** for when it is.

### R3 — Credential/token revocation MUST be immediate, not TTL-only (design-rule + tracked gap)

A revoked credential MUST stop being honored **before** its natural TTL. Relying on
short TTL alone means a compromised/rotated credential keeps working for the whole
TTL window. (Evidence: Langfuse #14359 — cached API keys, including negative-cache
misses, survived revocation until TTL; they added a flush-all + negative-cache purge.)

- **Admin API keys** need an invalidation seam (a revocation check / cache flush), not
  TTL reliance.
- **Kernel-signed plugin tokens (LIVE CODE, real gap).** H3/H7c short-TTL service
  tokens + identity assertions + frontend tokens are verified purely by signature +
  `exp` today (`pkg/pluginproto`, `plugintoken`). There is **no revocation seam**: the
  moment a plugin must be forcibly cut off mid-session (compromised plugin, emergency
  disable), a still-unexpired token keeps verifying until it expires. The
  supervisor's `disabled` state stops *delivering* new tokens but does not *revoke*
  outstanding ones. This is a genuine gap on shipped code. **Tracked as issue #63
  (linked to the auth arc #21); not built in K2.** A minimal fix is a
  per-plugin/`jti` denylist consulted in the verify path, or a signer-key rotation
  scoped to a plugin. The in-memory signing key (lite) already invalidates *all*
  tokens on kernel restart — that is the current blunt break-glass.
  - **Named trigger (#63):** *immediate revocation is required before any real
    multi-tenant / production deployment; TTL-only is acceptable ONLY at single-tenant
    pilot scale.* This item gates the pilot→production transition for the plugin-token
    surface — it must be closed (or consciously waived for a single-tenant pilot)
    before onboarding untrusted plugins or multiple tenants.

### R4 — Limiters: availability fail-open, resource-protection fail-closed (validated)

A rate/quota limiter whose backend is unavailable must **fail open** (allow) — a
limiter that fails closed when its Redis is down becomes a worse single point of
failure than the thing it protects (Langfuse #14329). But a **resource-protection**
gate (ingest backpressure protecting the DB) must **fail closed** (shed). We already
do both correctly: ingest sheds 503 under overload/persist-unhealthy (Arc G,
`ingest/receiver.go`), and the login limiter is in-process (no backend-down path).
Pinned so any *future distributed* limiter/quota keeps the distinction: *"backend
unavailable → allow; actually over limit → reject,"* and stays observable when it
degrades.

### R5 — Agent-callable tools are privileged + scope-gated (design-rule)

For the MCP server (F4) and any agent-callable plugin surface: a tool that enumerates
users/members or touches identity is **privileged**, gated by an explicit scope, and
**never** a default agent-key capability. The permission intersection (H3) applies to
**agent tokens exactly as to sessions** — an agent key is not a bypass. (Evidence:
Langfuse #14523 reverted a `listUsers` MCP tool that exposed the user directory to
in-app agent keys; and a bundled permission-SQL fix was lost in the revert — don't
bundle authz fixes into revertable feature PRs.)

### R6 — Enforce invariants at the convergence seam, not per-caller (→ `CLAUDE.md`)

The single most valuable pattern from the mine, and already how our best code works
(the persist stage, the Query API `auth()` intersection, the plugin-token verify):
**enforce an invariant at the one seam every path funnels through, never per-caller —
new callers must inherit it by construction, not by remembering to re-check.** Pinned
in `CLAUDE.md` design principles so it survives contributors without the arc history.

### R7 — Self-hosting rules (immune today; audit on introduction)

We are immune to these now (verified: no secure-context crypto, no server redirects,
no payload re-parse, request bodies capped, Go marshal has no V8 string-length trap).
Each is a rule to honor **when** the surface is introduced, for the "self-host under
plain HTTP + `PUBLIC_PATH` subpath" pillar:

- **No secure-context-only browser crypto** in frontend paths self-hosters run over
  HTTP: `crypto.randomUUID`/`crypto.subtle` throw off-`localhost` HTTP. Use a library
  fallback. (#14498)
- **Subpath-aware redirects.** Any future server-issued redirect / `Location` header
  (an OIDC callback is the likely first) MUST prepend the deployment base path.
  Client-side react-router already is `PUBLIC_PATH`-aware. (#14397)
- **Precision-preserving payload parse.** A future payload pretty-printer MUST NOT
  round-trip user JSON through `JSON.parse` (loses integers > 2^53); render the
  opaque string or use a lossless parser. (#14449)
- **Typed 4xx for oversized responses.** If a Query API response can grow huge, cap it
  (pre-serialization) and return a typed 4xx, don't let the serializer 500. (#14398)

## Consequences

- The cost-derivation rules (R1) constrain the future enrich stage; pinned in
  `06-usage-cost.md` §7 where its author looks.
- R2/R3/R5 constrain the future outbound + auth surfaces; the plugin-author security
  docs carry R2, this ADR carries R3's tracked gap.
- R6 becomes a standing `CLAUDE.md` principle.
- Nothing here changes runtime behavior; K2 is spec/ADR/docs + banked fixtures.

## Deferred (tracked, not built in K2)

- **Plugin-token revocation seam (R3) — tracked as issue #63.** The one real gap on
  live code. Implement a verify-path denylist (per-plugin / `jti`) or plugin-scoped key
  rotation so a `disabled` plugin's outstanding tokens stop verifying immediately, not
  at TTL. **Gates pilot→production** (see R3's named trigger).
- **The outbound-fetch surface itself (R2)** — kernel evals/webhooks + the plugin
  egress watchdog land with the feature that needs them; the rules pre-commit the
  shape.
- **Banked dialect fixtures** — OpenInference (cost/usage precedence, tool-call
  shapes) and Flue framework span trees are recorded under
  `kernel/testdata/fixtures/deferred/` for the day those normalizers are built; they
  are not wired to conformance (no normalizer exists).
