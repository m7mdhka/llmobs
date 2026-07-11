# SSO API (v1alpha1) — OIDC login & per-org provider config (#21)

Normative contract for Arc O / O5. See ADR-0034. SSO is OSS core — never plan-gated.

## Trust guarantee

An IdP-asserted identity is untrusted until the callback verifies, IN ORDER: browser **state**
(CSRF) → code exchange → **ID-token** signature/iss/aud/exp (against the IdP JWKS) → **nonce**
(replay). No claim (email, groups) is read until all pass. A role/identity from an unverified
assertion never grants access.

## Login flow (unauthenticated)

### `GET /auth/sso/providers`
Lists orgs offering SSO, for the login page. `200 → {"providers":[{"org_id","name","issuer"}]}`.
No secrets.

### `GET /auth/sso/{org}/start`
Begins the auth-code flow: sets an HttpOnly state+nonce cookie and `302`s to the IdP authorize
endpoint. `404` if the org has no enabled provider.

### `GET /auth/sso/{org}/callback?code=&state=`
Completes the flow. On full verification + a mapped group, JIT-provisions the user and mints a
session (`302 → /`). Rejections (never a session): `403` state/nonce mismatch, `401`
exchange/verify failure or unverified email, `403` no mapped group (**no access**, not a
fallback), `403` revoked user.

## Authorization from groups

An SSO user's role = the **highest** role mapped from their verified IdP groups. No mapped group
→ no access. The role is set as membership in the callback's org only (target-tenant), capped
strictly-below the configurer, never owner, never downgrading an existing owner.

## Provider config (authenticated, org:manage in `{org}`)

### `GET /v1alpha1/sso/{org}`
Returns the secret-free config: `{org_id, issuer, client_id, group_claim,
local_password_disabled, enabled, group_roles}`. `404` if none.

### `PUT /v1alpha1/sso/{org}`
Body `{issuer, client_id, client_secret?, group_claim?, local_password_disabled?, enabled?,
group_roles}`. Requires **org:manage** (owner) in `{org}`. Each `group_roles` entry must be
strictly-below the configurer's own role → `403` otherwise (so SSO can't grant owner). Disabling
local passwords is `409` unless an owner with a local password remains (break-glass). Omitting
`client_secret` preserves the stored one. `204` on success.

## Local-password interaction

When `local_password_disabled` is set for an org, only OWNERS may still log in with a local
password (break-glass); everyone else must use SSO. SSO-provisioned users have no local password
and can never log in locally. The last owner can never be locked out.
