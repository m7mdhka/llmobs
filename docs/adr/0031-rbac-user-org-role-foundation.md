# ADR-0031: Real auth foundation — users, orgs, memberships, resource:verb RBAC

- **Status:** Accepted (Arc O / O1 — the auth foundation)
- **Date:** 2026-07-11
- **Deciders:** m7mdhka (Arc O)
- **Amends:** ADR-0005 (single-admin local auth, PR-D1) — the flat `users.role` becomes a
  per-org membership role.
- **Relates to:** ADR-0023 (the double-token intersection this RBAC plugs into — the
  scope vocabulary is shared), the permission model (`perm` package). Built from the #21
  SSO/RBAC harvest (Langfuse #14438 resource:verb, #14448 provisioning-authz,
  #14540 execute-verb). Sets up O2 (intersection), O3 (provisioning), O4 (revocation),
  O5 (SSO), O6 (per-user state).

## Context

LLMObs shipped single-admin auth: a flat `users.role` string (`'admin'` by default), a
`perm.RoleScopes(role)` that returned the full set for `admin` and a read-metadata viewer
for anything else. Fine for a solo pilot; not production. Real auth needs multiple users,
a proper role model, and — the load-bearing insight — a role vocabulary that is the SAME
currency the double-token intersection already computes in, so a human role and a plugin
grant intersect without translation.

The existing architecture made this a small, surgical change: **every** authorization
decision already funnels through `perm.RoleScopes(sess.User.Role)` (five production call
sites) into one convergence seam (`query.Server.auth`). So the foundation is (1) a
membership model, (2) a real Role→Scope[] map, (3) a session that resolves the membership
role — after which all five sites become role-aware at once.

## Decision

### R1 — Per-org membership, not a flat role

A user's authority is a **membership**: `org_memberships(user_id, org_id, role)`. A user
has zero authority in an org they don't belong to (`RoleInOrg` → `""` → no scopes, fail
closed). The legacy `users.role` is retained unread for rollback safety and backfilled
into memberships (the bootstrap admin → org `owner`); a fresh install's first admin is
made owner by `BootstrapAdmin`.

### R2 — Four roles, a strict ladder (owner ⊃ admin ⊃ member ⊃ viewer)

| Role | Data scopes | Management scopes |
|---|---|---|
| **owner** | all (read metadata+payloads, write scores, ingest, delete) | `members:manage`, `org:manage` |
| **admin** | all | `members:manage` |
| **member** | read metadata+payloads, read scores, write scores, `actions:execute` | — |
| **viewer** | read metadata, read scores | — |

Rationale for the cuts: **viewer** is a read-only observer (no payloads — the sensitive
prompt/completion content — no writes); **member** is a contributor (sees payloads to do
their work, annotates via scores, may run actions) but administers nothing; **admin**
manages members + all data; **owner** additionally holds org-level actions (delete org,
transfer). `traces:write` (ingest) and `traces:delete` (GDPR erasure) are owner/admin — a
member does not ingest via a session (that's an API key) or erase data.

### R3 — Two scope axes, one set

- **Data permissions** (`traces:read.metadata`, `scores:write`, …) are the **shared
  currency**: a role's data scopes and a plugin's manifest grant are the same vocabulary,
  so `role-scopes ∩ plugin-grant ∩ project` is well-defined (O2). A scope means the same
  thing whether it gates a human or a plugin.
- **Management permissions** (`members:manage`, `org:manage`, `actions:execute`) are
  **RBAC-only**: never in a plugin token, so they never enter the data intersection; they
  gate control-plane administration directly (provisioning, O3). The two axes live in one
  scope list but never collide — no data op requires a management perm, no plugin grant
  contains one.

### R4 — Server-resolved role governs; no plan gating

The role is always resolved server-side from `org_memberships` (a DB join in
`ResolveSession`), never from a client-supplied claim. There is **no plan gating anywhere**
— RBAC/SSO/provisioning ship in the Apache-2.0 core (the fully-OSS wedge; both incumbents
gate enterprise auth behind a paid plan).

## Consequences

- **All five role→scope sites become real at once** with no signature change:
  `perm.RoleScopes` stays `role → scopes`; only its body (the static map) and the role
  string it's given (now the membership role) change.
- **Data-preserving.** Existing installs upgrade via the 0017 backfill; the bootstrap
  contract (first admin) is unchanged, now also granting an owner membership.
- **Sets up the arc:** O2 wires per-project-org role resolution into the convergence seam
  and proves the full role × resource:verb × project matrix; O3 gates provisioning on
  `members:manage`; O6 adds per-user state keyed on the resolved user.

## Alternatives considered

- **Keep the flat `users.role`, add finer roles to it.** Rejected: a global role can't
  express "admin of org A, viewer of org B" — the whole point of the membership layer.
- **A separate RBAC scope vocabulary distinct from plugin capabilities.** Rejected: it
  would force a translation layer at the intersection and let a scope mean different things
  for a human vs a plugin. One currency is the invariant.
- **Per-resource ACLs (row-level grants).** Out of scope: the role model is the ruled
  granularity; finer is a future decision, not this arc.
