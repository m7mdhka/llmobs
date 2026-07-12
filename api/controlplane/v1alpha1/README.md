# Control-plane API (v1alpha1)

Normative contract for the LLMObs control plane: authentication, tenancy (users /
orgs / projects / memberships), role-based authorization, provisioning, credential
revocation, and SSO. Everything here is **server-derived and fail-closed** — a
client-supplied role / org / user-id / identity claim is never trusted — and every
capability ships in the Apache-2.0 core with no plan gating.

`roles.json` (this directory) is the machine-readable Role → Scope[] map the kernel
mirrors; a conformance test asserts `perm.RoleScopes` equals it exactly. This document
is the human-readable companion to that map and to the OpenAPI/JSON-Schema contracts.

## Contents

1. [RBAC model](#1-rbac-model) — The user / organization / membership / role model and the resource:verb scope vocabulary.
2. [Provisioning API](#2-provisioning-api) — Endpoints that change who has authority — create org, invite / set-role / remove member, and credential minting.
3. [Revocation API](#3-revocation-api) — Immediate credential invalidation — a revoked credential is denied on the very next request, before its TTL.
4. [SSO API](#4-sso-api) — OIDC login and per-org identity-provider configuration. OSS core, never plan-gated.

---

## 1. RBAC model

*The user / organization / membership / role model and the resource:verb scope vocabulary.*

Normative. The user/organization/membership/role model and the resource:verb scope
vocabulary. `roles.json` (this directory) is the machine-readable Role → Scope[] map the
kernel mirrors; a conformance test asserts `perm.RoleScopes` equals it exactly. See
[ADR-0031](../../../docs/adr/0031-rbac-user-org-role-foundation.md).

### Entities

- **user** — an account: `{ id, email, password_hash (argon2id) }`. Authenticates via a
  server-side opaque session (cookie hash + per-session CSRF). A user's *authority* is
  NOT on the user — it is per-org (below).
- **organization** — a tenant boundary: `{ id, name }`. Owns projects.
- **project** — a data scope within an org: `{ id, org_id, name }`. Telemetry, api-keys,
  and settings are project-scoped.
- **membership** — `(user_id, org_id) → role`. The authoritative source of a user's
  authority in an org. A user with no membership in an org has **zero** authority there.

### Roles (a strict ladder)

`viewer ⊂ member ⊂ admin ⊂ owner` — each holds a superset of the one below.

| Role | May… | Data scopes | Management scopes |
|---|---|---|---|
| **viewer** | read metadata + scores (no payloads) | `traces:read.metadata`, `scores:read` | — |
| **member** | + read payloads, write scores, run actions | + `traces:read.payloads`, `scores:read.payloads`, `scores:write`, `actions:execute` | — |
| **admin** | + ingest/erase data, manage members | all data | `members:manage` |
| **owner** | + org-level (delete org, transfer) | all data | + `org:manage` |

### Scope vocabulary (resource:verb)

Two axes in one scope list:

- **Data permissions** — the SHARED currency with plugin grants (a scope means the same
  thing for a human or a plugin, so `role-scopes ∩ plugin-grant ∩ project` is
  well-defined): `traces:read.metadata`, `traces:read.payloads`, `traces:write`
  (ingest), `traces:delete` (GDPR erasure), `scores:read`, `scores:read.payloads`,
  `scores:write`.
- **Management permissions** — RBAC-only (never in a plugin token, never intersected;
  they gate control-plane administration directly): `members:manage` (invite / set-role /
  remove — the provisioning gate, O3), `org:manage` (owner-only org actions),
  `actions:execute` (run action-type ops — jobs/evals; reserved, gates future ops).

Additive-only: new scopes and roles extend the sets; existing entries are never
repurposed. A scope absent from a role is denied; a role absent from the map (or empty)
grants **nothing** (fail closed).

### Authority resolution (server-derived, never client-supplied)

The **target** resolution, per request:

1. A request's project → its org (`OrgForProject`).
2. The session user's membership role in that org (`RoleInOrg`; `""` if none).
3. `perm.RoleScopes(role)` → the canonical scope set.
4. The convergence seam intersects those scopes with any plugin grant and the project.
   Payload-level reads are gated by `traces:read.payloads` / `scores:read.payloads`.

> **O1 vs O2.** O1 (this PR) ships the model and resolves the role once — from the
> **default org** — into the session (`ResolveSession`), which is exact for the single-org
> lite/scale profiles it targets. Steps 1–2 (per-**project** org → membership role at the
> request seam) are wired by **O2**, which stops populating an ambient `sess.User.Role`
> and resolves `RoleInOrg(user, OrgForProject(project))` where the request's project is
> known. The helpers (`OrgForProject`, `RoleInOrg`) exist now so O2 is a rewire, not a
> redesign. Until then, treat `sess.User.Role` as the default-org role, not a per-project
> authority.

In all cases the role is resolved from `org_memberships` server-side; a client-supplied
role / org / user-id claim is never trusted. No plan gating anywhere — the full model
ships in the Apache-2.0 core.

---

## 2. Provisioning API

*Endpoints that change who has authority — create org, invite / set-role / remove member, and credential minting.*

Normative contract for the Arc O / O3 provisioning endpoints. All are session-authenticated
(cookie) and CSRF-protected on mutating methods. The actor is always the server-resolved
session; a client-supplied actor id is never read. See ADR-0032 for the rationale.

### Authorization model (the invariants a client can rely on)

- **Member provisioning requires `members:manage` in the TARGET org.** The org is the `{org}`
  path segment; authority is resolved there, never against the caller's default org. A
  non-member of `{org}` is forbidden regardless of authority held elsewhere.
- **Strictly below your own.** A caller may assign only a role strictly below their own, and
  may modify/remove only a member strictly below their own. Assigning/affecting a role at or
  above your own is `403`. Owners are therefore never modifiable or removable via these
  endpoints (last-owner protection, structural).
- **create-org and global pricing require instance-admin** (an owner of the default org). The
  per-project pricing discount requires configuration authority in that project's org.
- Roles: `owner` > `admin` > `member` > `viewer`. Only these four are assignable.

### Endpoints

#### `GET /v1alpha1/orgs`
List the caller's own memberships. `200 → {"orgs":[{"org_id","role"},...]}`.

#### `POST /v1alpha1/orgs`  — create-org
Body `{"name": string}`. Requires instance-admin. The caller becomes the new org's owner.
`201 → {"org_id","name","role":"owner"}` · `400` missing name · `403` not instance-admin.

#### `POST /v1alpha1/orgs/{org}/members`  — invite / add member
Body `{"email": string, "role": owner|admin|member|viewer, "initial_password"?: string}`.
Requires `members:manage` in `{org}` and `role` strictly below the caller's. If no account
exists for `email`, one is created (`initial_password` required; an existing user's password
is NEVER changed by invite). `201 → {"user_id","role","created_user"}` · `400` invalid role /
missing password for a new user · `403` insufficient authority or role too high · `409`
already a member (use set-role).

#### `PUT /v1alpha1/orgs/{org}/members/{userId}`  — set-role
Body `{"role": owner|admin|member|viewer}`. Requires `members:manage` in `{org}`, the target
strictly below the caller, and the new role strictly below the caller. `200 →
{"user_id","role"}` · `403` insufficient authority / target or role at-or-above caller ·
`404` target not a member.

#### `DELETE /v1alpha1/orgs/{org}/members/{userId}`  — remove-member
Requires `members:manage` in `{org}` and the target strictly below the caller. `204` ·
`403` target at-or-above caller · `404` target not a member.

### Credential provenance & scope-capping (api-keys)

`POST`/`DELETE /v1alpha1/api-keys` require configuration authority in the key's project org
(a viewer cannot mint or revoke keys — this is a floor, not "admin-only": any role with write
authority qualifies). Additionally, **a minted key can never carry authority the minter does
not personally hold**: each requested coarse scope must be granted by the minter's own role
(`ingest`→`traces:write`, `delete`→`traces:delete`, `query:payloads`→`traces:read.payloads`,
etc.). A member (no `traces:write`/`traces:delete`) minting `ingest` or `delete` is `403`; a
mixed request is rejected whole (no partial mint). This mirrors the frontend token, which is
capped to the session role the same way. Created keys record `created_by_user_id`
(`ON DELETE SET NULL`): deleting the creator drops the provenance link but never the key.

---

## 3. Revocation API

*Immediate credential invalidation — a revoked credential is denied on the very next request, before its TTL.*

Normative contract for Arc O / O4 immediate revocation. See ADR-0033.

### Guarantee

A revoked credential is denied on the **very next request**, before its TTL — never at expiry.
Revocation cascades down the derivation tree: revoking a principal denies every credential
derived from it.

### The derivation tree and what a revocation denies

- **user** → revoking a user denies, immediately and atomically: their sessions, the API keys
  they minted (`created_by_user_id`), and their still-live plugin **frontend tokens** and
  **identity assertions**; and blocks their re-login.
- **plugin** → disabling a plugin denies its already-issued **service token** (and any
  identity/frontend token for it) immediately; re-enabling clears the revocation.
- **API key** → deleting a key denies its bearer on the next request (existing behavior).
- **jti** → a single token can be denied individually (primitive; no UI endpoint yet).

### Endpoints

#### `POST /v1alpha1/users/{userId}/revoke` — revoke a user
Body (optional) `{"reason": string}`. Requires **instance-admin** (an owner of the default
org). `204` on success · `403` not instance-admin · `404` unknown user. Idempotent.

#### `DELETE /v1alpha1/api-keys/{publicKey}` — revoke an API key
(Existing.) Requires configuration authority in the key's project org. `204`. Immediate.

### Verification semantics (for token verifiers)

A kernel-signed plugin token is denied if any of `{jti, plugin, deriving-user-email}` appears
in the revocation store with `revoked_at >= token.issued_at`. The check **fails closed**: if
the revocation store cannot be consulted, the token is denied (401/403), never admitted. This
is enforced inside `plugintoken.Signer.Verify*`, which every signed-token path funnels through.

---

## 4. SSO API

*OIDC login and per-org identity-provider configuration. OSS core, never plan-gated.*

Normative contract for Arc O / O5. See ADR-0034. SSO is OSS core — never plan-gated.

### Trust guarantee

An IdP-asserted identity is untrusted until the callback verifies, IN ORDER: browser **state**
(CSRF) → code exchange → **ID-token** signature/iss/aud/exp (against the IdP JWKS) → **nonce**
(replay). No claim (email, groups) is read until all pass. A role/identity from an unverified
assertion never grants access.

### Login flow (unauthenticated)

#### `GET /auth/sso/providers`
Lists orgs offering SSO, for the login page. `200 → {"providers":[{"org_id","name","issuer"}]}`.
No secrets.

#### `GET /auth/sso/{org}/start`
Begins the auth-code flow: sets an HttpOnly state+nonce cookie and `302`s to the IdP authorize
endpoint. `404` if the org has no enabled provider.

#### `GET /auth/sso/{org}/callback?code=&state=`
Completes the flow. On full verification + a mapped group, JIT-provisions the user and mints a
session (`302 → /`). Rejections (never a session): `403` state/nonce mismatch, `401`
exchange/verify failure or unverified email, `403` no mapped group (**no access**, not a
fallback), `403` revoked user.

### Authorization from groups

An SSO user's role = the **highest** role mapped from their verified IdP groups. No mapped group
→ no access. The role is set as membership in the callback's org only (target-tenant), capped
strictly-below the configurer, never owner, never downgrading an existing owner.

### Provider config (authenticated, org:manage in `{org}`)

#### `GET /v1alpha1/sso/{org}`
Returns the secret-free config: `{org_id, issuer, client_id, group_claim,
local_password_disabled, enabled, group_roles}`. `404` if none.

#### `PUT /v1alpha1/sso/{org}`
Body `{issuer, client_id, client_secret?, group_claim?, local_password_disabled?, enabled?,
group_roles}`. Requires **org:manage** (owner) in `{org}`. Each `group_roles` entry must be
strictly-below the configurer's own role → `403` otherwise (so SSO can't grant owner). Disabling
local passwords is `409` unless an owner with a local password remains (break-glass). Omitting
`client_secret` preserves the stored one. `204` on success.

### Local-password interaction

When `local_password_disabled` is set for an org, only OWNERS may still log in with a local
password (break-glass); everyone else must use SSO. SSO-provisioned users have no local password
and can never log in locally. The last owner can never be locked out.
