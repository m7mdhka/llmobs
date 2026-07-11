// Package perm is the canonical permission vocabulary the double-token
// intersection operates in (ADR-0023/R3). Two scope vocabularies exist in the
// system — coarse api-key/session verbs (query, query:payloads, scores:write,
// delete, ingest) and fine-grained manifest-permission nouns (traces:read.
// metadata, traces:read.payloads, …). They cannot intersect directly, so ONE must
// be canonical.
//
// The fine-grained noun vocabulary is canonical, and the coarse verbs translate
// UP into it. This direction is deliberate and load-bearing: "capabilities are
// nouns about data, never verbs about features" is a founding invariant, and
// translating toward the richer vocabulary is lossless and additively extensible
// (a new permission — scores:read distinct from write, store:read.collection_x —
// just gets named), whereas collapsing down to the five coarse verbs is a one-way
// door that would cap the permission model at today's operations.
package perm

import "strings"

// Canonical DATA permissions (resource:verb nouns about data). These are the SHARED
// CURRENCY: a human role's data scopes and a plugin's manifest grant are the same
// vocabulary, so the double-token intersection (role-scopes ∩ plugin-grant ∩ project)
// operates on one set. Additive: new permissions extend this set; existing ones are
// never repurposed.
const (
	TracesReadMetadata = "traces:read.metadata"
	TracesReadPayloads = "traces:read.payloads"
	TracesWrite        = "traces:write"  // ingest telemetry
	TracesDelete       = "traces:delete" // GDPR erasure
	ScoresRead         = "scores:read"
	ScoresReadPayloads = "scores:read.payloads" // e.g. the score comment
	ScoresWrite        = "scores:write"
)

// MANAGEMENT permissions (Arc O / O1) gate control-plane administration — provisioning
// members, org-level actions. They are RBAC-ONLY: a plugin token never carries one, so
// they never enter the data intersection (a plugin can't administer the org). They gate
// provisioning handlers directly (O3). Kept in the same scope set as data perms because a
// role is one scope list; the two axes never collide because no data op requires a
// management perm and — ENFORCED, not merely asserted — no plugin grant contains one
// (IsManagementPerm rejects it at plugin-grant admission, and DataPermsOnly strips it from
// any role scopes handed toward a plugin).
const (
	MembersManage  = "members:manage"  // invite / set-role / remove members (O3)
	OrgManage      = "org:manage"      // owner-only org-level actions (delete org, transfer)
	ActionsExecute = "actions:execute" // run action-type ops (jobs/evals) — reserved, gates future ops (#14540)
)

var managementScopes = map[string]bool{MembersManage: true, OrgManage: true, ActionsExecute: true}

// IsManagementPerm reports whether a scope is a management (RBAC-only) permission. A
// plugin grant MUST NOT contain one — the plugin-grant admission seam rejects it, so the
// "a plugin never holds a management scope" invariant is enforced by construction (O1
// added these scopes to owner/admin, so the guard must exist wherever a manifest grant is
// loaded or role scopes are handed toward a plugin).
func IsManagementPerm(s string) bool { return managementScopes[s] }

// DataPermsOnly returns the scopes with all management (and capability-marker) scopes
// removed — the set safe to hand toward a plugin credential. Belt-and-suspenders alongside
// admission-time rejection: even if a role carries management scopes (owner/admin do), an
// identity assertion or intersection handed to a plugin can never carry one.
func DataPermsOnly(scopes []string) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		if IsManagementPerm(s) || IsCap(s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

var all = []string{
	TracesReadMetadata, TracesReadPayloads, TracesWrite, TracesDelete,
	ScoresRead, ScoresReadPayloads, ScoresWrite,
}

// All returns the full canonical DATA permission set (the ceiling a data intersection can
// grant). It does NOT include management perms — those are role-granted, never
// intersected. `owner`/`admin` roles hold All() data plus their management perms.
func All() []string { return append([]string(nil), all...) }

// coarseExpand translates each coarse api-key/session verb UP into the canonical
// permissions it grants. query:payloads is additive over query (a payload-capable
// key holds both).
var coarseExpand = map[string][]string{
	"ingest":         {TracesWrite},
	"query":          {TracesReadMetadata, ScoresRead},
	"query:payloads": {TracesReadPayloads, ScoresReadPayloads},
	"scores:write":   {ScoresWrite},
	"delete":         {TracesDelete},
}

// ExpandCoarse translates a set of coarse api-key/session scopes into the union of
// their canonical permissions. Unknown scopes contribute nothing.
func ExpandCoarse(coarse []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range coarse {
		for _, p := range coarseExpand[c] {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// opRequires maps a Query API operation (named by its historical coarse verb at
// the call site) to the canonical permission the caller must hold.
var opRequires = map[string]string{
	"query":          TracesReadMetadata,
	"query:payloads": TracesReadPayloads,
	"scores:write":   ScoresWrite,
	"delete":         TracesDelete,
	"ingest":         TracesWrite,
}

// RequiredPermForOp returns the canonical permission an operation requires.
func RequiredPermForOp(op string) string { return opRequires[op] }

// Roles (Arc O / O1). A user's authority in an org is one of these; the resolved role
// (from org_memberships, server-derived) maps to a canonical scope set via RoleScopes.
// The ladder is strict: viewer ⊂ member ⊂ admin ⊂ owner (each holds a superset).
const (
	RoleOwner  = "owner"  // full control incl. org-level (delete/transfer) — the org's first admin
	RoleAdmin  = "admin"  // all data + manage members; not org-level
	RoleMember = "member" // read all + write scores + run actions; no administration
	RoleViewer = "viewer" // read metadata + read scores; no payloads, no write, no administration
)

// roleScopes is the static Role → Scope[] map (the banked #21 design). It is the ONE
// place a role becomes permissions; there are no bare `role == "admin"` checks anywhere
// else. A role absent from this map resolves to NO scopes — FAIL CLOSED: an unknown or
// empty role (e.g. a user with no membership in the requested org) can do nothing, never
// silently a viewer.
var roleScopes = map[string][]string{
	RoleOwner: append(All(), MembersManage, OrgManage, ActionsExecute),
	RoleAdmin: append(All(), MembersManage, ActionsExecute),
	RoleMember: {
		TracesReadMetadata, TracesReadPayloads,
		ScoresRead, ScoresReadPayloads, ScoresWrite,
		ActionsExecute,
	},
	RoleViewer: {TracesReadMetadata, ScoresRead},
}

// RoleScopes returns a role's canonical permissions from the static map, or an empty set
// for an unknown/empty role (fail closed). Derived, never a hardcoded verb list; the
// same currency a plugin grant is expressed in, so the intersection is well-defined.
func RoleScopes(role string) []string {
	s, ok := roleScopes[role]
	if !ok {
		return nil
	}
	return append([]string(nil), s...)
}

// ValidRole reports whether role is one of the defined roles. Provisioning (O3) rejects
// any role not in this set; it is the allow-list for an assignable role.
func ValidRole(role string) bool {
	_, ok := roleScopes[role]
	return ok
}

// Roles returns the defined roles, highest-privilege first (owner→viewer). Stable order
// for UIs and the assignable-role list.
func Roles() []string { return []string{RoleOwner, RoleAdmin, RoleMember, RoleViewer} }

// roleRank orders roles by privilege for the "cannot assign a role above your own"
// provisioning rule (O3). Higher = more privileged. An unknown role ranks -1 (below all).
var roleRank = map[string]int{RoleViewer: 0, RoleMember: 1, RoleAdmin: 2, RoleOwner: 3}

// RoleAtLeast reports whether role a is at least as privileged as role b (a ⊇ b in the
// ladder). Used so a principal can only assign a role ≤ their own (no escalation).
func RoleAtLeast(a, b string) bool {
	ra, oka := roleRank[a]
	rb, okb := roleRank[b]
	return oka && okb && ra >= rb
}

// HasWriteAuthority reports whether a permission set carries ANY write scope —
// the coarse "may this role mutate, not just read?" question. Used to gate
// configuration writes (plugin settings, J2) the same way the supervisor gates
// enable/disable: admins today, refined by the #21 RBAC seam. Derived from the
// scopes, never a hardcoded role string.
func HasWriteAuthority(scopes []string) bool {
	return Has(scopes, TracesWrite) || Has(scopes, ScoresWrite) || Has(scopes, TracesDelete)
}

// --- capability markers ---
//
// A plugin's service token carries BOTH data permissions (canonical nouns) and
// capability markers (which primitive it may call) — capabilities gate the
// endpoint, permissions gate the data. Markers are prefixed so the two axes never
// collide in one scope set.

const capPrefix = "cap:"

// CapMarker renders a capability as a token scope, e.g. "cap:query".
func CapMarker(capability string) string { return capPrefix + capability }

// IsCap reports whether a scope is a capability marker.
func IsCap(s string) bool { return strings.HasPrefix(s, capPrefix) }

// SplitCapsAndPerms partitions a plugin's token scopes into capability markers
// and data permissions.
func SplitCapsAndPerms(scopes []string) (caps, perms []string) {
	for _, s := range scopes {
		if IsCap(s) {
			caps = append(caps, s)
		} else {
			perms = append(perms, s)
		}
	}
	return caps, perms
}

// opCap maps a Query API operation to the plugin capability it requires. delete
// has no capability — plugins may not perform GDPR erasure.
var opCap = map[string]string{
	"query":          "query",
	"query:payloads": "query",
	"scores:write":   "write",
	"ingest":         "ingest",
}

// CapForOp returns the capability an op requires of a plugin ("" => no plugin may
// perform it).
func CapForOp(op string) string { return opCap[op] }

// Has reports whether perms contains p (p == "" is never held).
func Has(perms []string, p string) bool {
	if p == "" {
		return false
	}
	for _, x := range perms {
		if x == p {
			return true
		}
	}
	return false
}
