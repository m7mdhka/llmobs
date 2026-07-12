package perm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// TestRoleScopesMatchContract is the contracts-first conformance check: the kernel's
// static Role→Scope[] map MUST equal api/controlplane/v1alpha1/roles.json exactly, so the
// spec and the code can never drift (the same discipline as the manifest/model mirrors).
func TestRoleScopesMatchContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "api", "controlplane", "v1alpha1", "roles.json"))
	if err != nil {
		t.Fatalf("read roles.json: %v", err)
	}
	var contract struct {
		Roles map[string][]string `json:"roles"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("parse roles.json: %v", err)
	}
	if len(contract.Roles) == 0 {
		t.Fatal("roles.json declares no roles")
	}
	for role, want := range contract.Roles {
		got := RoleScopes(role)
		ws, gs := append([]string(nil), want...), append([]string(nil), got...)
		sort.Strings(ws)
		sort.Strings(gs)
		if !reflect.DeepEqual(ws, gs) {
			t.Fatalf("role %q scopes drift:\n contract=%v\n code=%v", role, ws, gs)
		}
	}
	// And no code role is missing from the contract.
	for _, role := range Roles() {
		if _, ok := contract.Roles[role]; !ok {
			t.Fatalf("code role %q is not in roles.json", role)
		}
	}
}

// TestRoleLadderStrict proves the ladder viewer ⊂ member ⊂ admin ⊂ owner: each role's
// scopes are a strict superset of the one below, so no lower role can do anything a higher
// one can't.
func TestRoleLadderStrict(t *testing.T) {
	ladder := []string{RoleViewer, RoleMember, RoleAdmin, RoleOwner}
	for i := 1; i < len(ladder); i++ {
		lower, higher := set(RoleScopes(ladder[i-1])), set(RoleScopes(ladder[i]))
		for s := range lower {
			if !higher[s] {
				t.Fatalf("%s holds %q but %s does not — ladder broken", ladder[i-1], s, ladder[i])
			}
		}
		if len(higher) <= len(lower) {
			t.Fatalf("%s must be a STRICT superset of %s", ladder[i], ladder[i-1])
		}
	}
}

// TestProveNegatives is the role-scope prove-the-negative matrix at the map level.
func TestProveNegatives(t *testing.T) {
	// A viewer cannot perform any write/admin action.
	viewer := RoleScopes(RoleViewer)
	for _, denied := range []string{TracesWrite, TracesDelete, ScoresWrite, TracesReadPayloads, MembersManage, OrgManage} {
		if Has(viewer, denied) {
			t.Fatalf("viewer must NOT hold %q", denied)
		}
	}
	if HasWriteAuthority(viewer) {
		t.Fatal("viewer must have no write authority")
	}
	// A member cannot administer or ingest/erase.
	member := RoleScopes(RoleMember)
	for _, denied := range []string{MembersManage, OrgManage, TracesWrite, TracesDelete} {
		if Has(member, denied) {
			t.Fatalf("member must NOT hold %q", denied)
		}
	}
	// An admin can manage members but not org-level.
	admin := RoleScopes(RoleAdmin)
	if !Has(admin, MembersManage) {
		t.Fatal("admin must hold members:manage")
	}
	if Has(admin, OrgManage) {
		t.Fatal("admin must NOT hold org:manage (owner-only)")
	}
	// FAIL CLOSED: an unknown or empty role grants nothing (not silently a viewer).
	if s := RoleScopes("superuser"); len(s) != 0 {
		t.Fatalf("unknown role must grant NO scopes, got %v", s)
	}
	if s := RoleScopes(""); len(s) != 0 {
		t.Fatalf("empty role (no membership) must grant NO scopes, got %v", s)
	}
	// A role can only assign a role at or below its own (no escalation).
	if RoleAtLeast(RoleAdmin, RoleOwner) {
		t.Fatal("admin must NOT be able to assign owner")
	}
	if !RoleAtLeast(RoleAdmin, RoleMember) {
		t.Fatal("admin must be able to assign member")
	}
	if RoleAtLeast(RoleMember, RoleAdmin) {
		t.Fatal("member must NOT be able to assign admin")
	}
	if !ValidRole(RoleOwner) || ValidRole("root") {
		t.Fatal("ValidRole must accept defined roles and reject others")
	}
}

// TestDataPermsOnlyStripsManagement proves the enforced negative: no management scope
// survives DataPermsOnly, so a plugin credential (which is always built from a
// DataPermsOnly'd grant/role) can never carry members:manage/org:manage — even under an
// owner session or a manifest that declares one. This is the compensating guard added
// alongside widening the role scope set.
func TestDataPermsOnlyStripsManagement(t *testing.T) {
	// An owner's scopes include management perms; the plugin-facing form must not.
	filtered := DataPermsOnly(RoleScopes(RoleOwner))
	for _, mgmt := range []string{MembersManage, OrgManage, ActionsExecute} {
		if Has(filtered, mgmt) {
			t.Fatalf("DataPermsOnly must strip %q", mgmt)
		}
	}
	// Data perms survive.
	if !Has(filtered, TracesReadMetadata) || !Has(filtered, ScoresWrite) {
		t.Fatal("DataPermsOnly must keep data perms")
	}
	// A malicious manifest grant declaring management perms yields none after the strip.
	badGrant := []string{TracesReadMetadata, OrgManage, MembersManage}
	got := DataPermsOnly(badGrant)
	if Has(got, OrgManage) || Has(got, MembersManage) {
		t.Fatalf("a manifest-declared management perm must be stripped, got %v", got)
	}
	if !IsManagementPerm(OrgManage) || IsManagementPerm(TracesWrite) {
		t.Fatal("IsManagementPerm must classify management vs data perms correctly")
	}
	// Cap markers are also stripped (they belong on the capability axis, not perms).
	if Has(DataPermsOnly([]string{CapMarker("query"), TracesReadMetadata}), CapMarker("query")) {
		t.Fatal("DataPermsOnly must strip capability markers")
	}
}

func set(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// TestRoleAboveStrictlyBelow is the crown-jewel cap at the map level: a
// principal may act on / assign only roles STRICTLY below their own. RoleAbove is the
// mechanism; this pins its full truth table, including the fail-closed edges ("" and
// unknown roles, which appear for non-members).
func TestRoleAboveStrictlyBelow(t *testing.T) {
	// Same role is NEVER "above" itself — no lateral clone, no self-escalation.
	for _, role := range Roles() {
		if RoleAbove(role, role) {
			t.Fatalf("%s must not be strictly above itself", role)
		}
	}
	// Strictly-below pairs hold; strictly-above pairs do not.
	above := [][2]string{{RoleOwner, RoleAdmin}, {RoleOwner, RoleViewer}, {RoleAdmin, RoleMember}, {RoleMember, RoleViewer}}
	for _, p := range above {
		if !RoleAbove(p[0], p[1]) {
			t.Fatalf("%s must be strictly above %s", p[0], p[1])
		}
		if RoleAbove(p[1], p[0]) {
			t.Fatalf("%s must NOT be strictly above %s", p[1], p[0])
		}
	}
	// Nobody outranks an owner — the structural last-owner protection.
	for _, role := range Roles() {
		if RoleAbove(role, RoleOwner) {
			t.Fatalf("%s must not be able to act on an owner", role)
		}
	}
	// Fail closed on the non-member/unknown edges (both sides): "" and undefined roles are
	// neither above nor below anything, so the rule never lets them through.
	for _, bad := range []string{"", "superuser", "root"} {
		if RoleAbove(RoleOwner, bad) {
			t.Fatalf("owner must not be 'above' an unknown role %q (fail closed)", bad)
		}
		if RoleAbove(bad, RoleViewer) {
			t.Fatalf("unknown role %q must not be 'above' a viewer (fail closed)", bad)
		}
	}
}

// TestKeyScopeGrantedBy pins the api-key mint cap: a coarse key scope is
// grantable only if the minter's role holds EVERY canonical perm it expands to — a key never
// carries authority its minter lacks. This is the amplification fix both reviews caught.
func TestKeyScopeGrantedBy(t *testing.T) {
	owner := RoleScopes(RoleOwner)
	member := RoleScopes(RoleMember)
	viewer := RoleScopes(RoleViewer)
	// Owner (All()) may mint every coarse scope.
	for _, sc := range []string{"ingest", "query", "query:payloads", "scores:write", "delete"} {
		if !KeyScopeGrantedBy(owner, sc) {
			t.Fatalf("owner must be able to mint %q", sc)
		}
	}
	// Member holds read+scores:write, NOT traces:write/traces:delete.
	grant := map[string]bool{"query": true, "query:payloads": true, "scores:write": true, "ingest": false, "delete": false}
	for sc, want := range grant {
		if KeyScopeGrantedBy(member, sc) != want {
			t.Fatalf("member mint %q = %v, want %v", sc, !want, want)
		}
	}
	// Viewer may mint only the metadata read key.
	if !KeyScopeGrantedBy(viewer, "query") {
		t.Fatal("viewer must be able to mint a read-only query key")
	}
	for _, sc := range []string{"query:payloads", "scores:write", "ingest", "delete"} {
		if KeyScopeGrantedBy(viewer, sc) {
			t.Fatalf("viewer must NOT be able to mint %q", sc)
		}
	}
	// Unknown / empty coarse scopes are never grantable (fail closed).
	if KeyScopeGrantedBy(owner, "wat") || KeyScopeGrantedBy(owner, "") {
		t.Fatal("unknown coarse scope must not be grantable")
	}
}
