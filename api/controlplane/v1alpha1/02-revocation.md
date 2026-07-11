# Revocation API (v1alpha1) — immediate credential invalidation (#63)

Normative contract for Arc O / O4 immediate revocation. See ADR-0033.

## Guarantee

A revoked credential is denied on the **very next request**, before its TTL — never at expiry.
Revocation cascades down the derivation tree: revoking a principal denies every credential
derived from it.

## The derivation tree and what a revocation denies

- **user** → revoking a user denies, immediately and atomically: their sessions, the API keys
  they minted (`created_by_user_id`), and their still-live plugin **frontend tokens** and
  **identity assertions**; and blocks their re-login.
- **plugin** → disabling a plugin denies its already-issued **service token** (and any
  identity/frontend token for it) immediately; re-enabling clears the revocation.
- **API key** → deleting a key denies its bearer on the next request (existing behavior).
- **jti** → a single token can be denied individually (primitive; no UI endpoint yet).

## Endpoints

### `POST /v1alpha1/users/{userId}/revoke` — revoke a user
Body (optional) `{"reason": string}`. Requires **instance-admin** (an owner of the default
org). `204` on success · `403` not instance-admin · `404` unknown user. Idempotent.

### `DELETE /v1alpha1/api-keys/{publicKey}` — revoke an API key
(Existing.) Requires configuration authority in the key's project org. `204`. Immediate.

## Verification semantics (for token verifiers)

A kernel-signed plugin token is denied if any of `{jti, plugin, deriving-user-email}` appears
in the revocation store with `revoked_at >= token.issued_at`. The check **fails closed**: if
the revocation store cannot be consulted, the token is denied (401/403), never admitted. This
is enforced inside `plugintoken.Signer.Verify*`, which every signed-token path funnels through.
