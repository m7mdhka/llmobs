# Control-plane RBAC model (v1alpha1) — users, orgs, memberships, roles

Normative. The user/organization/membership/role model and the resource:verb scope
vocabulary. `roles.json` (this directory) is the machine-readable Role → Scope[] map the
kernel mirrors; a conformance test asserts `perm.RoleScopes` equals it exactly. See
[ADR-0031](../../../docs/adr/0031-rbac-user-org-role-foundation.md).

## Entities

- **user** — an account: `{ id, email, password_hash (argon2id) }`. Authenticates via a
  server-side opaque session (cookie hash + per-session CSRF). A user's *authority* is
  NOT on the user — it is per-org (below).
- **organization** — a tenant boundary: `{ id, name }`. Owns projects.
- **project** — a data scope within an org: `{ id, org_id, name }`. Telemetry, api-keys,
  and settings are project-scoped.
- **membership** — `(user_id, org_id) → role`. The authoritative source of a user's
  authority in an org. A user with no membership in an org has **zero** authority there.

## Roles (a strict ladder)

`viewer ⊂ member ⊂ admin ⊂ owner` — each holds a superset of the one below.

| Role | May… | Data scopes | Management scopes |
|---|---|---|---|
| **viewer** | read metadata + scores (no payloads) | `traces:read.metadata`, `scores:read` | — |
| **member** | + read payloads, write scores, run actions | + `traces:read.payloads`, `scores:read.payloads`, `scores:write`, `actions:execute` | — |
| **admin** | + ingest/erase data, manage members | all data | `members:manage` |
| **owner** | + org-level (delete org, transfer) | all data | + `org:manage` |

## Scope vocabulary (resource:verb)

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

## Authority resolution (server-derived, never client-supplied)

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
