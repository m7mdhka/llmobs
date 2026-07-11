# Provisioning API (v1alpha1) — orgs & memberships

Normative contract for the Arc O / O3 provisioning endpoints. All are session-authenticated
(cookie) and CSRF-protected on mutating methods. The actor is always the server-resolved
session; a client-supplied actor id is never read. See ADR-0032 for the rationale.

## Authorization model (the invariants a client can rely on)

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

## Endpoints

### `GET /v1alpha1/orgs`
List the caller's own memberships. `200 → {"orgs":[{"org_id","role"},...]}`.

### `POST /v1alpha1/orgs`  — create-org
Body `{"name": string}`. Requires instance-admin. The caller becomes the new org's owner.
`201 → {"org_id","name","role":"owner"}` · `400` missing name · `403` not instance-admin.

### `POST /v1alpha1/orgs/{org}/members`  — invite / add member
Body `{"email": string, "role": owner|admin|member|viewer, "initial_password"?: string}`.
Requires `members:manage` in `{org}` and `role` strictly below the caller's. If no account
exists for `email`, one is created (`initial_password` required; an existing user's password
is NEVER changed by invite). `201 → {"user_id","role","created_user"}` · `400` invalid role /
missing password for a new user · `403` insufficient authority or role too high · `409`
already a member (use set-role).

### `PUT /v1alpha1/orgs/{org}/members/{userId}`  — set-role
Body `{"role": owner|admin|member|viewer}`. Requires `members:manage` in `{org}`, the target
strictly below the caller, and the new role strictly below the caller. `200 →
{"user_id","role"}` · `403` insufficient authority / target or role at-or-above caller ·
`404` target not a member.

### `DELETE /v1alpha1/orgs/{org}/members/{userId}`  — remove-member
Requires `members:manage` in `{org}` and the target strictly below the caller. `204` ·
`403` target at-or-above caller · `404` target not a member.

## Credential provenance & scope-capping (api-keys)

`POST`/`DELETE /v1alpha1/api-keys` require configuration authority in the key's project org
(a viewer cannot mint or revoke keys — this is a floor, not "admin-only": any role with write
authority qualifies). Additionally, **a minted key can never carry authority the minter does
not personally hold**: each requested coarse scope must be granted by the minter's own role
(`ingest`→`traces:write`, `delete`→`traces:delete`, `query:payloads`→`traces:read.payloads`,
etc.). A member (no `traces:write`/`traces:delete`) minting `ingest` or `delete` is `403`; a
mixed request is rejected whole (no partial mint). This mirrors the frontend token, which is
capped to the session role the same way. Created keys record `created_by_user_id`
(`ON DELETE SET NULL`): deleting the creator drops the provenance link but never the key.
