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

// Canonical permissions (nouns about data). Additive: new permissions extend this
// set; existing ones are never repurposed.
const (
	TracesReadMetadata = "traces:read.metadata"
	TracesReadPayloads = "traces:read.payloads"
	TracesWrite        = "traces:write"  // ingest telemetry
	TracesDelete       = "traces:delete" // GDPR erasure
	ScoresRead         = "scores:read"
	ScoresReadPayloads = "scores:read.payloads" // e.g. the score comment
	ScoresWrite        = "scores:write"
)

var all = []string{
	TracesReadMetadata, TracesReadPayloads, TracesWrite, TracesDelete,
	ScoresRead, ScoresReadPayloads, ScoresWrite,
}

// All returns the full canonical permission set (admin/full grant).
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

// RoleScopes returns a user role's canonical permissions. admin => the full set
// (the seam where issue #21 RBAC defines finer roles); anything else is a
// read-metadata viewer until RBAC lands. Derived, never a hardcoded verb list.
func RoleScopes(role string) []string {
	if role == "admin" {
		return All()
	}
	return []string{TracesReadMetadata, ScoresRead}
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
