# ADR-0035: Per-user scope for the `kv` primitive (O6)

- **Status:** Accepted (Arc O / O6 — closes #69)
- **Date:** 2026-07-12
- **Deciders:** m7mdhka (Arc O)
- **Builds on:** ADR-0031 (the O1 resolved-user identity), ADR-0023 (the double-token
  assertion carrying the user Sub). Closes #69 (per-user state, folded from Arc N).

## Context

The `kv` primitive stored plugin state scoped to `(plugin_id, project_id)` — shared across every
user of a project. Plugins (and plugin frontends mounted in the shell) also need **per-user**
state: dashboard layout, column choices, a user's own saved views. That state must be isolated
per user, and — the whole point of the arc — scoped by the **server-resolved** acting user, not
by anything the client supplies.

## Decision

Add a `user_id` dimension to the `kv` store: `''` = project scope (the existing shared behavior,
the default), a non-empty value = per-user scope. The `plugin_kv` primary key becomes
`(plugin_id, project_id, user_id, key)` (migration 0021 — additive; existing rows default to `''`
= project scope, a no-op for current data).

Each `kv` call carries a `scope` of `"project"` (default) or `"user"`. When `"user"`, the kernel
keys storage on the caller's **verified assertion subject** (`pluginauth.Caller.Subject`, the
O1-resolved user email) — **never a request-body field.** The plugin chooses the *scope*, not the
*user*. This is the O2/O4 lesson applied to a new tenant dimension: resolve against the acting
principal, at the one seam the request already funnels through (`pluginauth.Require` /
`RequireFrontend`), so no path can name another user.

The `Subject` is the same for a user across the backend (`session:`) and frontend (`frontend:`)
paths, so per-user state is consistent regardless of transport. A user-less credential (a bare
service token) has an empty `Subject` and cannot use `user` scope — the call is rejected.

`pluginsettings` (project-shared plugin config) always passes `user_id = ''` — settings are not
per-user; per-user state is the `kv` primitive's job.

## Consequences

- One user's per-user state is unreadable and unwritable by another, even in the same project
  with the same key — proven at the HTTP handler (fake store) and against the real Postgres PK.
- The SDK gains an optional `{ scope }` on `kv.get/set/delete/list` — additive, semver-safe.
- Project scope is unchanged and remains the default, so every existing plugin keeps working.
- Deferred: a per-user variant of `settings` (schema-validated per-user config) if a real need
  appears; today per-user state uses plain `kv`.
