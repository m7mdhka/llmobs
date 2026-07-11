# ADR-0024: Plugin settings store + SchemaForm

- **Status:** Accepted (implementation — Arc J, J2)
- **Date:** 2026-07-10
- **Deciders:** m7mdhka (Arc J — close the Tier-3 yellows)
- **Relates to:** ADR-0023 (plugin protocol, frontend token / J1), ADR-0008 (SDK
  primitives — settings is `kv` made frontend-reachable, not a ninth primitive),
  ADR-0005 (double-token auth), ADR-0002 (dogfood rule). Closes Set-B yellow **B9**
  (settings schema-form tab) and unblocks B2/B4 frontend persistence.

## Context

A plugin manifest may declare `spec.frontend.settingsSchema` — a path to a JSON
Schema for the plugin's settings — but nothing rendered it and nothing stored the
values. A settings UX was hand-rolled per plugin, and a *pure-frontend* plugin (no
backend) had nowhere confined to persist configuration at all. J1 (the frontend
token) now gives a frontend a least-privilege identity; J2 uses it to give every
plugin a settings tab with **zero settings-specific backend code**.

Two capabilities are needed: **render** (JSON Schema → a form) and **persist**
(store the values, scoped to the plugin + tenant, with secrets that never come back).

## Decision

### 1. Settings is `kv` made frontend-reachable — NOT a ninth primitive

Settings are configuration *data* (a noun), not a feature. Rather than mint a new
capability, the settings store is the existing `kv` primitive (ADR-0023/H4)
specialized and exposed on a **frontend-reachable** endpoint. The eight primitives
(ADR-0008 + `store`) are unchanged; `settings` is a schema-aware view over `kv`.

- **Endpoint:** `POST /v1alpha1/plugin/settings/get` and `/set`, mirroring the other
  plugin-data endpoints (POST-only, capped body).
- **Auth:** the **J1 frontend token** (`RequireFrontend`) — a pure-frontend plugin
  reaches it with the credential the shell already mints. The plugin id and project
  come from the token (audience + `projectId`), never the request body, so a plugin
  can only read/write **its own** settings in **its own** tenant. (A backend may also
  manage settings via the same store; the frontend path is the new seam.)
- **Writes require configuration authority.** Settings are **project-shared** plugin
  config (including secrets), so a state-changing `set` must not be allowed to a
  read-only viewer who happens to hold a frontend token. The token proves *which*
  plugin/tenant; a second check — the caller's **session role** carrying a write
  scope — proves *may they administer it*. This is the same split the supervisor uses
  for enable/disable: **admins today, refined by the #21 RBAC seam**. Reads (`get`)
  are open to any valid frontend token for the plugin (config is low-sensitivity and
  secrets are never returned). Per-user (rather than project-shared) settings are a
  future dimension, not needed to close B9.
- **Storage:** the plugin `kv` store, under a single reserved document key per
  `(plugin_id, project_id)`. No new table, no new infra — lite and scale identical.

`/v1alpha1/plugin/settings` is an identity/data endpoint (kernel-owned config data),
not a verb-about-a-feature — consistent with invariant #4 the same way `store` is.

### 2. Secrets: JSON Schema `writeOnly` — write-only, envelope-encrypted, never returned

A settings field marked **`"writeOnly": true`** (standard JSON Schema 2020-12; we do
not invent a keyword) is a **secret**:

- **On set:** its value is envelope-encrypted with the kernel master key (the same
  `secretbox` used by the `secrets` primitive) before it touches storage. Plaintext
  is never persisted or logged.
- **On get:** the ciphertext is **never** decrypted back to the client. The response
  carries only a boolean marker per secret field (`{"<field>": {"set": true}}`) so
  the form can render "•••• (set)" without ever holding the value. **This is the
  prove-the-negative: a written secret is never in any GET response.**
- **On set with the field absent or empty:** the existing encrypted value is
  **preserved** (re-saving the form does not wipe a secret the user didn't retype).

### 3. Validation on BOTH sides, over a deliberately small schema subset

The kernel never trusts the client, so it validates every `set`. A full JSON Schema
validator is a hot-path dependency we don't want (ADR/go-style: stdlib-first). We
therefore support a **small, explicit subset** and hand-roll a pure-function
validator for it (table-tested):

- types: `string`, `number`, `integer`, `boolean`;
- `enum` (string choices); `required`; `writeOnly` (secret); `minLength`/`maximum`
  where trivial; object with `properties` (one level — settings are flat).

The **same** subset drives `packages/schema-form`, so client and kernel agree.
Anything outside the subset is a manifest-conformance failure (caught before
install), not a runtime surprise. Promotion to a richer schema dialect is a future
arc; the subset is additive within it.

### 4. The kernel needs the schema — the registry loads it

To know which fields are secret (and to validate), the kernel reads the plugin's
`settingsSchema` file at scan time (the dir source already loads manifests) and
exposes it on the registry `Plugin` record. The settings handler looks it up by
plugin id (like `GrantFor`). A plugin with no `settingsSchema` has no settings tab.

## Consequences

- New public endpoint `/v1alpha1/plugin/settings` (get/set), documented in
  `api/plugin/v1alpha1`; frontend-token authed; kv-backed.
- New package `packages/schema-form` (React, uses `packages/ui`, tokens-only): renders
  the subset, validates client-side, and enforces the write-only secret UX.
- SDK gains a `useSettings()` hook + settings client that presents the frontend token.
- The `writeOnly`-secret rule is the settings analogue of the `secrets` primitive's
  never-return guarantee; both rest on the same `secretbox`.
- No new SDK primitive/capability — settings is `kv`, so ADR-0008's count stands.

## Alternatives considered

- **A ninth `settings` primitive.** Rejected — settings is data over `kv`; a new
  capability would inflate the primitive set for no new data class (invariant #4).
- **Full JSON Schema validator dependency.** Rejected on the hot path (stdlib-first);
  the flat subset covers real settings forms and keeps client/kernel in lockstep.
- **Returning masked secrets (e.g. last 4 chars).** Rejected — any echo is a leak
  surface; a boolean "set" marker is strictly safer and enough for the UX.

## Validated (K2 / ADR-0025)

The settings schema-form's flat subset **excludes `pattern` (regex)** entirely — a
decision the Langfuse mine independently validated: Langfuse shipped a bug (#14818)
where a Unicode-property regex in an *advertised* JSON Schema (an MCP tool schema)
silently degraded because JSON Schema `pattern` compiles without the ECMAScript `u`
flag, disabling the whole tool catalog. Pinned as a rule for this and any future
externally-consumed schema (MCP tool schemas included): **the advertised/serializable
schema MUST stay within the representable subset; strict validation is a separate
server-side layer, never expressed as an advertised `pattern`.**

## Amended (N2 / ADR-0030) — the custom-settings-view escape

The flat subset renders standard fields; a plugin needing a **custom** settings UI (a
rule-builder, a visual editor) could not persist its complex shape. N2 adds an escape
**without** widening the subset or the secret rule:

- A plugin declares `spec.settingsView: "custom"`. Schema mode (SchemaForm + flat-subset
  validation) stays the **default** — the 80% get consistency for free.
- In custom mode the plugin **mounts its own settings view** via the neutral frontend
  contract (ADR-0030) and reads/writes through the same `useSettings` hook + `/get`/`/set`
  endpoints. Non-secret values are stored as **opaque JSON** (any shape), bounded by a
  fixed ceiling (`pluginsettings.MaxValueBytes`, 64 KiB) so a plugin can't turn
  project-shared settings into unbounded storage.
- **Secrets are unchanged (H4 holds).** A `writeOnly` field declared in `settingsSchema`
  (optional in custom mode, read ONLY for its secret declarations) is still
  envelope-encrypted, preserved-on-empty, and **never returned** — proven in custom mode
  by `pluginsettings` + settings-handler tests. A secret name is never written to the
  plaintext values map, so it cannot be read back even as opaque data.
  A declared secret is excluded from the returned values on the READ seam AND reaped from
  the plaintext map on the WRITE seam, so even a field **reclassified** opaque→`writeOnly`
  across a manifest evolution can never be read back in the clear (invariant #11 — the
  guard lives on both convergence seams, not just write).
- The kernel does not validate the opaque shape (the custom view owns that); it enforces
  only valid-JSON, the secret-is-a-string rule, and the size ceiling. Conformance checks a
  custom plugin's schema only for valid `writeOnly` declarations, not the flat subset.

## Deferred

Per-field RBAC on settings; settings history/audit. (Richer schema dialect for the
*schema-form* path is subsumed by the custom-view escape above.) Tracked; not needed to
close B9.
