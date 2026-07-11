# ADR-0032: Provisioning authorization — one shared gate, resolved against the target org

- **Status:** Accepted (Arc O / O3 — the arc's most dangerous surface)
- **Date:** 2026-07-11
- **Deciders:** m7mdhka (Arc O)
- **Builds on:** ADR-0031 (the RBAC foundation — roles, `org_memberships`, `members:manage`,
  `org:manage`), ADR-0023 (the double-token intersection). Resolves the pricing
  ambient-authority residual the O2 boundary review flagged.
- **Relates to:** #14448 (SCIM/provisioning-authz), #63 (revocation, O4), #69 (per-user
  state, O6).

## Context

O1 built the role model; O2 made every DATA authorization resolve the actor's role
per-request against the org that owns the target project. O3 adds the endpoints that CHANGE
who has authority: create an org, invite a member, set a member's role, remove a member.
This is where an authorization bug stops being a read leak and becomes account-takeover — a
member who can grant themselves owner owns the tenant. The whole PR is one rule stated
carefully enough that no sibling can drift from it.

## Decision

### 1. One shared gate across every member-provisioning sibling

`invite`, `set-role`, and `remove` all enter `orgProvisioner(r, targetOrg)` as their FIRST
statement. It resolves the actor's role in the **target org** (the `{org}` path segment) and
requires `members:manage` there. No sibling is reachable with less; none is strictly more
powerful than another. A sibling added later MUST call it too — this is invariant #11 (guard
at the one convergence seam, never re-checked per caller) applied to provisioning. This is
the direct answer to the recurring SCIM-authz bug class (#14448): "is any provisioning
sibling missing the gate the others have?"

### 2. Authority is resolved against the org the action TARGETS

`orgProvisioner` resolves `RoleInOrg(actor, targetOrg)` — never the actor's ambient/default
org. An owner of org A with no membership in org B has zero provisioning authority in B.
This is the O2 plugin-settings bug's family (a gate that read the wrong tenant), and
provisioning is where it is most dangerous, so it is closed by construction: the tenant of
the action and the authority for it are always the same org.

### 3. Strictly below your own — the crown-jewel cap

A principal may **assign** only a role strictly below their own, and may **modify/remove**
only a member strictly below their own (`perm.RoleAbove`). Consequences:

- No self-escalation and no other-escalation: an admin cannot mint an admin or an owner.
- No lateral clone: an admin cannot mint another admin (which could then act independently).
- **Owners are immutable via provisioning.** Nothing outranks an owner, so no `set-role` or
  `remove` can touch one. That IS the last-owner protection, stated structurally: an org can
  never be stripped of its owner by any provisioning call. (`CountOwners` is retained as a
  defense-in-depth belt if the rule ever loosens.) Ownership transfer / self-demotion is a
  deliberate future owner-only (`org:manage`) flow, not an oversight of O3.

### 4. create-org and global pricing gate on an explicit instance-admin

`create-org` has no target org to resolve against — it CREATES one. It gates on
`instanceAdmin` (an owner of the default org), and the creator becomes the new org's owner.
Self-serve org creation is a deliberate future opt-in, not a default: on a shared instance,
unbounded org creation by any user is a resource and blast-radius concern.

The same `instanceAdmin` axis resolves the **O2 pricing residual**. The global price table is
one instance-wide table shared by every tenant; editing it (or re-pricing history off a
superseded version) is genuinely instance-level, so it gates on `instanceAdmin` — an explicit
authority, not an ambient per-org role wearing global clothes. The per-project **discount**
(tenant config) gates on `writeAuthorityInProject`, resolved against the discount's own
project org, exactly like plugin settings. Naming the two axes is the resolution: a per-org
role governs per-org actions; only the default-org owner governs instance-wide ones.

### 5. The actor is always server-derived; credentials carry provenance

Every handler reads the actor from the session (`SessionFrom`) — a client-supplied actor id
is never trusted. Machine API keys now record `created_by_user_id` (`ON DELETE SET NULL`, so
deleting the creator never deletes a still-valid tenant's keys nor orphans the row), and key
mint/revoke are gated on `writeAuthorityInProject` so a read-only viewer cannot mint a
credential to escalate past their own role.

## Consequences

- The provisioning surface has exactly one authorization gate per family, at the entry seam;
  a new sibling inherits it by construction or fails review.
- No principal can grant or hold a role they could not already reach — the account-takeover
  path is closed and proven by an exhaustive role × sibling escalation matrix.
- Two ambient-authority residuals (settings in O2, pricing here) are both eliminated; there
  is no remaining gate that resolves authority against an org other than the one it targets.
- Deferred (documented, not forgotten): ownership transfer / self-demotion, self-serve org
  creation, and admin-initiated password reset (a separate credential flow with its own
  strictly-below cap) land in later arc work.
