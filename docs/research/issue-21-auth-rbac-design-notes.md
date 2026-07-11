# Issue #21 (auth: users, roles/RBAC, OIDC/SSO, project mgmt) — design notes harvested from the Langfuse mine

**Status: design input, not a commitment.** This is the auth-arc harvest from the
Langfuse merged-PR mine (400 merged PRs, #14317–#14991) — the auth/RBAC/SSO patterns
Langfuse actually shipped, distilled into a shape #21 can adopt or reject. Nothing
here is built. The strategic frame: **fully-OSS auth/RBAC/SSO is our wedge** — Langfuse
(ClickHouse-owned) gates SCIM/SSO behind a paid `admin-api` entitlement, and Opik
(Comet) gates enterprise auth; we never feature-gate. So the goal is to build the
enterprise-grade auth model *ungated by plan* — but with the authz rigor the mine
shows is non-negotiable.

Where this lands in our code (the #21 seams, all present today):
`perm.RoleScopes` (role→scope derivation), `authhttp/auth.go` (an OIDC login flow
mints the same `controlplane.Session`), `controlplane/apikeys.go` (`allowedScopes`,
where scoped/custom keys extend), and `query/server.go` (the fixed full-admin scope
that must become a real intersection).

## 1. RBAC scope model — `resource:verb` (the core shape) — evidence #14438, #14540

Adopt a `resource:verb` scope vocabulary, replacing our coarse admin/viewer:

- **Verbs: `read` / `CUD` / `execute`.** Reads and writes are **separate scopes**
  (`datasets:read` vs `datasets:CUD`) so a VIEWER reads but cannot mutate. A third
  **`execute` verb** exists for actions that *do* something beyond read/write —
  `playground:execute`, running an eval — because those spend money (outbound LLM) or
  trigger work even when they touch no stored resource. VIEWER gets `read`, not
  `execute` (#14540: a viewer must not run the playground and burn tokens).
- **Role → scopes is a static map** — a `Record<Role, Scope[]>` (`OWNER`/`ADMIN`/
  `MEMBER`/`VIEWER`/`NONE`), not scattered `if role==...` checks. Our `perm.RoleScopes`
  is exactly this seam; the change is granularity (from 2 scopes to `resource:verb`).
- **One scope gates API + route + UI.** The *same* scope constant is checked at the
  API handler, at the route/nav guard, and at the UI action — a single source of
  truth, so a permission can't be enforced in one place and forgotten in another. (Our
  invariant #11 / ADR-0025 R6 convergence-point principle applies.)

## 2. SCIM / user-provisioning authz — the HARD rule — evidence #14448

**This is a hard rule for the auth arc, not a suggestion.** Langfuse shipped a
vulnerability where SCIM `POST /Users` checked only for an org-scoped API key with
**no entitlement/authz gate** — so any org key could create accounts *with a
caller-set password* and assign roles up to **OWNER**, then mutate memberships: a full
account-takeover backdoor. The fix gated it, but the lesson for us is sharper because
we *don't* gate by plan:

- **User/membership/role provisioning is the single highest-privilege surface.**
  Every provisioning sibling — set-password, create-user, assign-role, invite-member,
  change-membership, SCIM Users CRUD — MUST be guarded by **one shared authz check at
  handler entry, before any I/O** (before the DB read that would even confirm the
  target exists, so the endpoint can't be probed).
- **Ungated by plan, strictly gated by authz.** Our OSS-ness means these endpoints are
  *available* to self-hosters (the wedge) — but "available" is not "unauthenticated."
  The authz check is the same one every admin endpoint shares; a *missing* gate on one
  sibling is the whole vulnerability. Audit that all provisioning endpoints funnel
  through the one guard (ADR-0025 R6).
- **Server-resolved subject MUST equal client-supplied identity** (#14790): when a
  mutation carries both a membership-id and a user-id, prove they refer to the same
  principal before any write — never let a client-supplied identity field diverge from
  the server-resolved subject, even when it isn't the authz key (it poisons the audit
  trail otherwise).

## 3. SSO / OIDC data model — evidence #14713

The tenancy/SSO data model to mirror (secrets always in a separate, never-exported
field):

- **`authMethods` per user** — a *derived* array: `"credentials"` (if a password is
  set) unioned with the user's federated providers (`google`, `okta`, …). A clean
  answer to "how does this user authenticate," computed, not stored.
- **`ssoConfig(domain, authProvider, …)`** — keyed by email domain + provider, with
  the OAuth **client secret in a separate field that is excluded from every export**
  (Langfuse keeps it in `authConfig` and omits that from the data export).
- **`verifiedDomain(organizationId, domain, verifiedAt)`** — domain-ownership
  verification as a first-class tenancy primitive linking an org to a domain.
- **Per-domain SSO enforcement as a first-class per-org record**, NOT an env var.
  Langfuse drives enforcement from `AUTH_DOMAINS_WITH_SSO_ENFORCEMENT` (an env list)
  and their own reviewer noted those rows lack timestamps *because* they're
  env-derived — promote it to a real per-org/per-domain "SSO required" record.
- An OIDC login flow lands at `authhttp/auth.go` and must mint the **same**
  `controlplane.Session` a password login does — SSO is a different front door to the
  identical session, not a parallel auth world. (And its callback redirect must be
  subpath-aware — ADR-0025 R7.)

## 4. API-key + token lifecycle — evidence #14724, #14359, #14325

- **Creator provenance** (#14724): record who minted a key as two nullable columns —
  `created_by_user_id` (human) and `created_by_api_key_id` (machine/org-key-minted),
  both `ON DELETE SET NULL` (deleting the creator nullifies the reference, never
  cascades to the created key). Nullish in any cache/token schema for forward-compat.
- **Revocation freshness** (#14359) — a revoked key/token MUST stop being honored
  **before** its TTL. This is pinned as a design-rule with a tracked gap in
  **ADR-0025 R3** (it is a *real* gap on our live kernel-signed plugin tokens, which
  are verified by signature + `exp` with no revocation seam). Centralize cache-key
  derivation in one helper (redaction + invalidation + scan all agree), cache
  *misses* too and purge them, and provide an operator flush-all break-glass.
- **Refresh-at-ratio** (#14325) — refresh a short-lived credential at a *ratio* of its
  remaining TTL (e.g. 0.8), not a fixed lead time, so it scales to any token lifetime;
  notify subscribers on rotation so a long-lived connection rebinds instead of failing
  mid-request. **Consider mirroring this in the SDK's plugin-token refresh** (H7c
  delivery is push-based; a ratio-based client refresh would complement it).

## 5. Agent-token gating — evidence #14523

For the MCP server (F4) and any agent-callable plugin surface: a tool that lists
users/members or touches identity is **privileged, scope-gated, and never a default
agent-key capability**. The H3 permission intersection applies to **agent tokens
exactly as to sessions** — an agent key is not an authz bypass. (Pinned as ADR-0025
R5.) And: don't bundle a permission/authz SQL fix inside a revertable feature PR — a
Langfuse revert silently undid a real cross-project membership fix.

## 6. Priority read

The mine maps the high-vote demand (Admin API #1007, custom key scopes #7104) to
issue #21 (`docs/research/langfuse-discussions/clusters.md`). The build order these
notes suggest: (1) the `resource:verb` scope model + static role map (turns
`perm.RoleScopes` from 2 scopes into real RBAC and makes `query/server.go`'s
fixed-admin grant a true intersection); (2) OIDC login minting the standard session;
(3) the SSO/domain data model; (4) SCIM under the one-shared-guard rule; (5) the
lifecycle rules (creator provenance, revocation seam, refresh-at-ratio). All ungated
by plan — the wedge.
