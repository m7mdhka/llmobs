# `kv` scopes — project vs per-user (O6)

The `kv` primitive is a per-plugin, tenant-scoped key/value store. Each call chooses a **scope**:

- **`project`** (default) — shared across every user of the project. Use it for plugin state
  that belongs to the workspace: cached lookups, shared configuration, team-wide saved views.
- **`user`** — private to the acting user, isolated within the project. Use it for per-user
  preferences: dashboard layout, column choices, a user's own saved views/filters.

```ts
// project scope (default) — shared
await kv.set("shared-view", view);
const shared = await kv.get("shared-view");

// per-user scope — private to whoever is signed in
await kv.set("layout", { columns: 3 }, { scope: "user" });
const mine = await kv.get("layout", { scope: "user" });
const myKeys = await kv.list("", { scope: "user" });
```

## What the kernel guarantees

For `user` scope, the kernel keys storage on the **server-resolved identity of the signed-in
user** (from the verified identity assertion) — **never a value your plugin supplies.** Your
plugin selects *the scope* (`"user"`), not *which user*. Consequences you can rely on:

- One user's per-user state is **never readable or writable by another user**, even in the same
  project with the same key.
- Per-user state and project state are **separate buckets**: a `user`-scoped `layout` and a
  `project`-scoped `layout` never collide.
- The same user sees the same per-user state whether your plugin reaches `kv` from its backend
  or its frontend — the identity is the user, not the transport.

A user-less credential (a bare service token, e.g. cold-path ingest) has no signed-in user and
therefore **cannot use `user` scope** — such a call is rejected. Per-user state only makes sense
on a request made on behalf of a user.
