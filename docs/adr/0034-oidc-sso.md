# ADR-0034: OIDC/SSO — the first external trust boundary, verify-before-trust

- **Status:** Accepted (Arc O / O5 — the arc's final, highest-risk PR)
- **Date:** 2026-07-12
- **Deciders:** m7mdhka (Arc O)
- **Builds on:** ADR-0031/0032 (RBAC, provisioning + the strictly-below cap), ADR-0033
  (revocation), ADR-0023 (secretbox). Addresses #21 (SSO/RBAC).

## Context

O1–O4 confined credentials WE derive (sessions, keys, plugin tokens) — the discipline "trust
server-derived identity, never client-supplied." O5 accepts identity asserted by *someone
else's* IdP: literally a client-supplied identity claim. It is the arc's ultimate test, and its
entire security is one property — **the IdP assertion is fully verified before ANY field in it
is read.**

## Decision

### Dependency: go-oidc + x/oauth2 (not hand-rolled crypto)

ID-token verification (RS256/ES256 signatures against a rotating JWKS, alg/kid confusion
defenses, iss/aud/exp checks) is exactly where hand-rolled JWT code grows subtle, catastrophic
bugs. We use the de-facto-standard `github.com/coreos/go-oidc/v3` (Apache-2.0) over
`golang.org/x/oauth2` (BSD-3), pulling `github.com/go-jose/go-jose/v4` (Apache-2.0)
transitively. All three are Apache-2.0/BSD — license-check clean (no AGPL/SSPL). This is a
COLD-path (login) dependency, not a hot-path one; per go-style "standard library first" the bar
is an ADR + license check, satisfied here. Rolling our own verifier would be more code to review
and a larger attack surface on the single most security-critical boundary in the system.

### Verify-before-trust ordering (the load-bearing security)

The callback does, IN THIS ORDER, and reads no claim until all pass:
1. **state** (CSRF): the query `state` must equal the value in the HttpOnly cookie set at
   `start` (constant-time compare). An attacker cannot forge a request carrying our cookie.
2. **code exchange**: server-to-server at the IdP token endpoint, authenticated with the sealed
   client secret.
3. **ID-token verify** (go-oidc): signature against the IdP JWKS, `iss` == the provider issuer,
   `aud` == our client_id, `exp`. Forged / expired / wrong-audience / wrong-issuer all fail here.
4. **nonce** (replay): the verified token's nonce must equal the one minted at `start`.
5. **only now** are `email` / `groups` read; an unverified `email_verified:false` is rejected.

### Authorization: role only from the verified group→role map, fail closed

An SSO user's role is the HIGHEST role among their verified IdP groups, via the per-org
group→role map. A user in NO mapped group gets **no access** — not a fallback role, not a
default. This is the only place an SSO role is decided, and only from verified input.

### JIT provisioning THROUGH the O3 discipline (the fourth provisioning path)

First login find-or-creates a passwordless user and sets membership in the **target org only**
(the org whose config matched — target-tenant). The role is capped **strictly-below the
configurer** at config-write time (`perm.RoleAbove`), so SSO can never be configured to grant
owner; JIT refuses the owner role as defense-in-depth and never downgrades an existing owner
(owners stay locally-managed break-glass accounts). A revoked user (O4) cannot re-enter via SSO.

### Config authority + local-password disable + last-owner protection

`GET/PUT /v1alpha1/sso/{org}` is gated on **org:manage in the target org** (owner-level;
resolved against the org it targets, O2). The client secret is sealed with secretbox and never
returned. Local passwords can be disabled per-org, but only OWNERS retain local login
(break-glass), and the flag is refused unless at least one owner still has a local password — so
the last owner can never be locked out even if the IdP is unreachable.

### No plan gating

SSO ships in the OSS core — the fully-OSS-auth wedge (both incumbents gate enterprise auth
behind a paid plan). Nothing here is feature-flagged by license.

## Consequences

- The single external trust boundary is verified before trust, proven by a mock-IdP test that
  rejects forged/expired/wrong-aud/wrong-iss/bad-nonce/bad-state/no-group assertions and never
  provisions or sessions on rejection.
- SSO users are passwordless `users` rows + `org_memberships` — everything downstream
  (ResolveSession, the intersection, revocation) works unchanged.
- **Operational**: `SECRETBOX_KEY` (base64 32-byte) must be set for SSO config to survive a
  restart (else the client secret is sealed with an ephemeral key). `PUBLIC_URL` fixes the
  redirect_uri origin (else derived from the Host header).
- **Deferred**: SAML (enterprise-legacy, XML-dsig — a follow-on; OIDC covers modern IdPs first),
  IdP-initiated logout / back-channel logout, and per-email-domain provider auto-discovery.
