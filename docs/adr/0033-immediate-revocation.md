# ADR-0033: Immediate credential revocation — the OUTLIVE half of the derived-credential invariant

- **Status:** Accepted (Arc O / O4 — closes #63)
- **Date:** 2026-07-12
- **Deciders:** m7mdhka (Arc O)
- **Builds on:** ADR-0023 (double-token intersection), ADR-0031/0032 (RBAC, provisioning,
  `created_by_user_id`). Closes #63 (immediate revocation).

## Context

Every credential the kernel issues was, before O4, revocable only two ways: delete the DB row
(sessions, API keys — checked every request, so immediate) or wait for TTL (the three
kernel-signed plugin tokens — service token, identity assertion, frontend token — verified by
signature + expiry ONLY, no store lookup). That TTL-only path is the #63 gap: a compromised
user's browser frontend token keeps working for its 5-minute TTL after you "revoke" them; a
disabled plugin's service token keeps calling kernel APIs for up to 10 minutes.

This is the dual of the derivation lesson Arc O kept relearning. O2/O3 proved a derived
credential must never **exceed** its deriver (resolve authority against the target tenant; cap
assigned roles and minted key scopes to the actor's own). O4 is the same invariant on the time
axis: **a derived credential must never OUTLIVE its deriver.** Revoking a principal must deny
every credential in its derivation subtree on the very next request.

## Decision

### One revocation authority, consulted at the one signed-token chokepoint

A `revocations` table (migration 0019) is an epoch/deny store keyed by principal:
`(principal_kind ∈ {user, plugin, jti}, principal_id, revoked_at)`. `controlplane.TokenLive`
answers "is this token still live?" — it is denied if its jti, its plugin, or its deriving
user (by email — the `Sub` every signed token already carries) appears with
`revoked_at >= token.issued_at`. Because the token was issued before the revoke instant, a
**still-within-TTL** credential is denied — the #63 crown case.

The check is injected into `plugintoken.Signer` and runs inside `VerifyServiceToken` /
`VerifyIdentityAssertion` / `VerifyFrontendToken`. **Every** signed-token verification funnels
through those three methods (the Query auth seam, the plugin-primitive seam, and jobs), so one
injected checker covers all paths **by construction** — invariant #11. No token or mint format
change was needed: the tokens already carry the user email (`Sub`), the plugin id (audience /
`PluginID`), and a jti. The check **fails closed**: a token whose liveness cannot be determined
(store error) is denied, so revocation cannot be bypassed by disrupting the store.

Sessions and API keys are already DB-looked-up every request, so they are revoked by row
DELETE (immediate) — no epoch needed for them.

### The cascade (the load-bearing correctness)

`RevokeUser` is one transaction that denies the user's whole subtree at once: DELETE their
sessions, DELETE the API keys they minted (`created_by_user_id`, O3), and write the user-epoch
revocation (which denies their still-live frontend tokens and identity assertions via
`TokenLive`). Login is then blocked (`UserRevoked`, checked in `VerifyPassword` after the
password compare so timing doesn't leak account state) so the subtree cannot be restarted. A
partial cascade would be exactly the #63 gap, so it is atomic.

Plugin-token revocation is wired to the supervisor: disabling a plugin records a plugin-epoch
revocation (its issued service token dies next verify, not at TTL); re-enable clears it.

### Endpoints / authority

`POST /v1alpha1/users/{userId}/revoke` gates on instance-admin (a global account-disable, like
create-org). API-key revoke keeps its per-project write-authority gate (O3). Plugin revocation
follows the supervisor's existing disable authority.

## Consequences

- A revoked credential is denied on the next request across every credential type — the #63
  requirement, proven by tests that would FAIL if the implementation waited for TTL.
- Every signed-token verify now does one indexed lookup against a tiny table. Acceptable (these
  paths already hit storage). A future in-process cache invalidated via the event bus is the
  optimization pass; it must not weaken cross-process immediacy.
- The epoch model supports reinstatement (delete the row); a token minted after the row is gone
  verifies normally. Un-revoke is not yet an endpoint (only the supervisor re-enable path uses
  it).
- Deferred: revoke-a-single-session and revoke-a-single-plugin-token endpoints (the jti kind
  and per-session delete exist as primitives; UIs are later), and persisted-key rotation
  (still the restart lever, ADR-0023).
