# Plugin protocol — v1alpha1 (normative)

This is the contract between the kernel and a **plugin backend**: an ordinary HTTP
service the operator runs, registered with the kernel by URL, that the kernel
discovers, handshakes with, supervises, and proxies user traffic to. It is the
Tier-3 surface — Tier-2 plugins (frontend-only, `query` + `surface`) need none of
it. Everything here is public and plugin-facing; a first-party plugin backend
consumes exactly these contracts and the Go types in
[`kernel/pkg/pluginproto`](../../../kernel/pkg/pluginproto) — never kernel
internals (the dogfood rule, ADR-0002).

Maturity: `v1alpha1`, additive-only within the version (ADR-0011).

## 0. Roles and the double token (ADR-0005 / ADR-0023)

- **Kernel** — mints tokens, supervises, proxies user traffic to the plugin.
- **Plugin backend** — an HTTP service reachable at `spec.backend.url`. It holds a
  **service token** (proving *which plugin it is* and *what it may do*), and it
  receives, on each proxied user request, a kernel-signed **identity assertion**
  (proving *who the end user is* and *what they may see*).

The kernel computes the **permission intersection** — `service-token scopes ∩
user-assertion scopes ∩ project scope` — on every plugin-reachable data call. A
plugin can never exceed either its own grant or the calling user's grant. The
kernel never trusts a plugin-supplied identity, and session cookies are **never**
forwarded to a plugin (the gateway proxy strips the cookie and injects the
assertion). Wiring is H3; this document fixes the formats.

## 1. Handshake — `GET {backend.url}{backend.healthPath}/../info` → `GET /plugin/v1/info`

On registration and on every (re)start, the kernel calls the plugin's info
endpoint. The plugin responds with [`handshake.schema.json`](handshake.schema.json):

- `id` — MUST equal the manifest `metadata.id`; a mismatch fails the handshake.
- `version` — the plugin's own semver.
- `pluginApiVersion` — the protocol version the plugin implements (e.g.
  `v1alpha1`). The kernel checks compatibility: the plugin's protocol major/
  maturity MUST be one the kernel supports, else the plugin goes `degraded` with a
  `incompatible_protocol` reason and no token is issued.
- `capabilities` — the plugin **echoes** the capabilities it believes it was
  granted. The kernel asserts this is a subset of the manifest's approved
  capabilities; a plugin claiming more than the manifest grants fails the
  handshake (defence in depth — the manifest, not the plugin, is authoritative).

The handshake is unauthenticated from the plugin's side (the kernel is reaching
*out* to a URL the operator configured); the kernel authenticates itself on all
subsequent proxied/data calls via the injected assertion + the plugin's ability
to verify the kernel signature (§4).

## 2. Health & functional watermark — `GET /plugin/v1/health`

Two-signal health, the Wave-1 lesson that *process-alive ≠ work-happening* (a
kernel that pinged only its DB pool once read healthy while unable to persist —
G2). A plugin reports [`health.schema.json`](health.schema.json):

- `live` (bool) — the process is up. Liveness.
- `ready` (bool) — dependencies are wired; the plugin can serve. Readiness.
- `watermark` (object, optional) — a **functional** signal that work is actually
  progressing: `{ lastProgressUnix, detail }`. For an ingest plugin this is the
  last time it accepted an event; for a job plugin, the last successful run. The
  supervisor uses `ready` for routing and the watermark for the `running` vs
  `degraded` distinction — a plugin that is `ready:true` but whose watermark has
  gone stale past its declared budget is `degraded`, not healthy.
- A **long-running job** MUST NOT read as degraded merely because it is busy: the
  watermark reports *progress*, not *idleness* (the Chiara/B3 lesson). A plugin
  advertises its watermark staleness budget in `spec.backend.watermarkBudget`.

## 3. Service-token issuance & refresh

On a successful handshake the kernel issues a **service token**
([`service-token.schema.json`](service-token.schema.json)): a compact, signed,
short-TTL credential scoped to the plugin's approved capabilities/permissions. The
plugin presents it on every call back to the kernel (ingest, kv, secrets, store,
events, jobs, score-write) in `Authorization: Bearer <service-token>`.

- **Delivery is kernel-initiated PUSH (H7c).** At handshake completion the kernel
  **POSTs** the token to the plugin's own registered URL —
  `POST {backend.url}/plugin/v1/token` with body
  `{ "serviceToken": "…", "expiresUnix": … }` — over the same operator-trusted
  channel as the health probes. There is **no plugin-pull endpoint**: a plugin
  cannot request a token, so "obtain a token for a different plugin id/project" is
  not an expressible operation — each plugin only ever receives, at its own URL,
  the token the kernel scoped to it.
- **Delivery is part of readiness, not a side channel.** A plugin that cannot
  receive its token (the POST fails) goes `degraded` and does **not** reach
  `running` — the token does not fail open.
- **TTL + refresh** — short (minutes, `exp`). Before expiry, while the plugin is
  `running`, the kernel mints a fresh token and re-delivers it by the same push; a
  `disabled` plugin's token is revoked and not re-delivered.
- **Scope** — `scopes` is the intersection of the plugin's manifest capabilities
  and permissions with what the operator approved at install. It is authoritative
  for the *plugin* half of the double-token intersection.

## 4. Identity-assertion header (kernel → plugin, per proxied request)

When a user hits `/api/plugins/{id}/*`, the kernel proxies to the plugin's backend
and injects `X-LLMObs-Identity-Assertion: <assertion>` — a kernel-signed,
audience-bound, short-TTL credential
([`identity-assertion.schema.json`](identity-assertion.schema.json)):

- `aud` — `plugin:{id}`. An assertion minted for plugin A MUST be rejected by
  plugin B (and by the kernel on the way back). Audience binding prevents token
  confusion.
- `scopes` — the **user's** effective scopes (their role/key ∩ project). This is
  the *user* half of the intersection.
- `projectId` — the tenant the request is scoped to.
- `exp` — short (minutes). The plugin SHOULD verify the signature and `exp`/`aud`
  using the kernel's public key (delivered at handshake; JWKS/rotation deferred,
  ADR-0023) via `pluginproto.VerifyIdentityAssertion`.

When the plugin calls **back** into the kernel (e.g. `query`) to act on behalf of
that user, it forwards the assertion alongside its service token; the kernel
recomputes the intersection server-side and never trusts the plugin's own claim
about who the user is.

## 5. Backend-route proxying — `/api/plugins/{id}/*`

The kernel exposes every running plugin backend under a stable, gateway-owned
prefix:

- `/api/plugins/{id}/<path>` → `{spec.backend.url}/<path>`, with the cookie
  stripped and the identity assertion injected (§4). The plugin never sees the
  session cookie; the kernel is the single place a cookie becomes an identity
  (the invariant, now code-defended because a plugin backend path exists).
- Only `running` plugins are proxied. A `degraded`/`disabled` plugin returns the
  SDK's unavailable-state response (503 with a typed body) so the shell can render
  a graceful degraded surface.
- The service token issued to the plugin is verifiable by the kernel on the return
  path; a request bearing a token for a `disabled`/unknown plugin is rejected.

## Token format (both tokens)

Compact, dependency-free, stdlib-verifiable (no JWT library on the hot path):

```
v1.<base64url(claimsJSON)>.<base64url(ed25519-signature)>
```

- Signature is `Ed25519.Sign(kernelPrivateKey, "v1." + base64url(claimsJSON))`.
- **`base64url` here is UNPADDED** (RFC 4648 §5 without `=` padding — Go
  `base64.RawURLEncoding`). A non-Go verifier whose base64 decoder requires padding
  (e.g. Python `urlsafe_b64decode`) MUST re-pad each segment to a multiple of 4
  before decoding. (Cross-language finding, H7.)
- Claims are UTF-8 JSON with the fields in the respective schema; times are unix
  seconds. Verification checks the Ed25519 signature, then `exp` (not past) and
  `aud` (expected audience). Ed25519 is asymmetric so a plugin can verify a
  kernel-minted assertion **without** holding a signing secret.
- **The kernel publishes its public key at `GET /v1alpha1/plugin/kernel-key`**
  (`{algorithm, public_key_hex, public_key_base64}`) — the handshake is
  kernel→plugin, so a plugin obtains the key here (H7 finding #2). JWKS endpoint +
  rotation is a deferred hardening pass (ADR-0023, issue).

The canonical types + `Sign`/`Verify` helpers live in `kernel/pkg/pluginproto`.
