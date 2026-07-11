package controlplane

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
)

// o3clean removes this test's rows from the shared DB (prefix usr_o3/org_o3/proj_o3).
func o3clean(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`DELETE FROM api_keys WHERE project_id LIKE 'proj_o3%'`,
		`DELETE FROM org_memberships WHERE org_id LIKE 'org_o3%' OR user_id LIKE 'usr_o3%'`,
		`DELETE FROM projects WHERE id LIKE 'proj_o3%' OR org_id LIKE 'org_o3%'`,
		`DELETE FROM sessions WHERE user_id LIKE 'usr_o3%'`,
		`DELETE FROM users WHERE id LIKE 'usr_o3%' OR lower(email) LIKE 'o3-%'`,
		`DELETE FROM organizations WHERE id LIKE 'org_o3%'`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("clean: %v", err)
		}
	}
}

// TestCreateOrgGrantsCreatorOwner: create-org atomically creates the org AND grants the
// creator the owner membership — an org is never born without an owner.
func TestCreateOrgGrantsCreatorOwner(t *testing.T) {
	pool := mpSetup(t)
	o3clean(t, pool)
	ctx := context.Background()
	seedUser(t, pool, "usr_o3_creator", "o3-creator@t")

	org, err := CreateOrg(ctx, pool, "Acme O3", "usr_o3_creator")
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	if org.ID == "" || org.Name != "Acme O3" {
		t.Fatalf("unexpected org %+v", org)
	}
	role, _ := RoleInOrg(ctx, pool, "usr_o3_creator", org.ID)
	if role != perm.RoleOwner {
		t.Fatalf("creator role = %q, want owner", role)
	}
	n, _ := CountOwners(ctx, pool, org.ID)
	if n != 1 {
		t.Fatalf("new org owner count = %d, want 1", n)
	}
	// An empty name is rejected.
	if _, err := CreateOrg(ctx, pool, "  ", "usr_o3_creator"); err == nil {
		t.Fatal("create-org with blank name must fail")
	}
}

// TestInviteMemberCreatesAndAdds covers the invite persistence contract: a NEW email creates
// a user (password required) + membership; an EXISTING user is added without touching their
// password; a double-invite is ErrAlreadyMember.
func TestInviteMemberCreatesAndAdds(t *testing.T) {
	pool := mpSetup(t)
	o3clean(t, pool)
	ctx := context.Background()
	seedOrg(t, pool, "org_o3a")

	// New user without a password → rejected (never a passwordless account).
	if _, _, err := InviteMember(ctx, pool, "org_o3a", "o3-new@t", perm.RoleMember, ""); err != ErrNewUserNoPassword {
		t.Fatalf("new user without password: got %v, want ErrNewUserNoPassword", err)
	}
	// New user WITH a password → created + membership.
	uid, created, err := InviteMember(ctx, pool, "org_o3a", "o3-new@t", perm.RoleMember, "hunter2pw")
	if err != nil || !created || uid == "" {
		t.Fatalf("invite new: uid=%q created=%v err=%v", uid, created, err)
	}
	if r, _ := RoleInOrg(ctx, pool, uid, "org_o3a"); r != perm.RoleMember {
		t.Fatalf("invited role = %q, want member", r)
	}
	// The created user can authenticate with the initial password (proves it was hashed+set).
	if _, err := VerifyPassword(ctx, pool, "o3-new@t", "hunter2pw"); err != nil {
		t.Fatalf("invited user must authenticate with the initial password: %v", err)
	}

	// Re-inviting the same user to the SAME org → ErrAlreadyMember (use set-role).
	if _, _, err := InviteMember(ctx, pool, "org_o3a", "o3-new@t", perm.RoleViewer, ""); err != ErrAlreadyMember {
		t.Fatalf("double invite: got %v, want ErrAlreadyMember", err)
	}

	// The SAME (existing) user invited into a DIFFERENT org → added, password untouched.
	seedOrg(t, pool, "org_o3b")
	uid2, created2, err := InviteMember(ctx, pool, "org_o3b", "o3-new@t", perm.RoleViewer, "")
	if err != nil || created2 || uid2 != uid {
		t.Fatalf("invite existing to new org: uid=%q created=%v err=%v", uid2, created2, err)
	}
	if _, err := VerifyPassword(ctx, pool, "o3-new@t", "hunter2pw"); err != nil {
		t.Fatalf("existing user's password must be unchanged by invite: %v", err)
	}
	// Invalid role is rejected by persistence too (defense in depth).
	if _, _, err := InviteMember(ctx, pool, "org_o3a", "o3-x@t", "root", "pw"); err != ErrInvalidRole {
		t.Fatalf("invalid role: got %v, want ErrInvalidRole", err)
	}
}

// TestRemoveMembership: removes an existing membership, and reports ErrNotMember for a
// user who is not in the org.
func TestRemoveMembership(t *testing.T) {
	pool := mpSetup(t)
	o3clean(t, pool)
	ctx := context.Background()
	seedOrg(t, pool, "org_o3a")
	seedUser(t, pool, "usr_o3_m", "o3-m@t")
	if err := SetMembership(ctx, pool, "usr_o3_m", "org_o3a", perm.RoleMember); err != nil {
		t.Fatal(err)
	}
	if err := RemoveMembership(ctx, pool, "usr_o3_m", "org_o3a"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if r, _ := RoleInOrg(ctx, pool, "usr_o3_m", "org_o3a"); r != "" {
		t.Fatalf("role after remove = %q, want empty", r)
	}
	if err := RemoveMembership(ctx, pool, "usr_o3_m", "org_o3a"); err != ErrNotMember {
		t.Fatalf("remove non-member: got %v, want ErrNotMember", err)
	}
}

// TestAPIKeyCreatorProvenanceSetNull: a key records its creator; deleting the creator sets
// created_by_user_id to NULL (SET NULL) and NEVER deletes the still-valid key.
func TestAPIKeyCreatorProvenanceSetNull(t *testing.T) {
	pool := mpSetup(t)
	o3clean(t, pool)
	ctx := context.Background()
	seedOrg(t, pool, "org_o3a")
	if _, err := pool.Exec(ctx, `INSERT INTO projects (id, org_id, name) VALUES ('proj_o3a','org_o3a','p') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	seedUser(t, pool, "usr_o3_creator", "o3-creator@t")

	_, pub, err := CreateAPIKey(ctx, pool, "proj_o3a", []string{"ingest"}, "usr_o3_creator", perm.RoleScopes(perm.RoleOwner))
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	var creator *string
	if err := pool.QueryRow(ctx, `SELECT created_by_user_id FROM api_keys WHERE public_key = $1`, pub).Scan(&creator); err != nil {
		t.Fatalf("read creator: %v", err)
	}
	if creator == nil || *creator != "usr_o3_creator" {
		t.Fatalf("created_by_user_id = %v, want usr_o3_creator", creator)
	}
	// Delete the creator → the key survives with a NULL provenance (SET NULL, not CASCADE).
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = 'usr_o3_creator'`); err != nil {
		t.Fatalf("delete creator: %v", err)
	}
	var still int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE public_key = $1 AND created_by_user_id IS NULL`, pub).Scan(&still); err != nil {
		t.Fatalf("recheck key: %v", err)
	}
	if still != 1 {
		t.Fatalf("key must survive creator deletion with NULL provenance, got %d", still)
	}
}

// TestAPIKeyScopeCappedToMinter is the fix for the mint-amplification HIGH both reviews
// caught: a key can never carry authority its minter lacks. At the CreateAPIKey seam, each
// requested coarse scope must be granted by the minter's own role — a member cannot mint an
// ingest (traces:write) or delete (traces:delete) key, though they CAN mint the read/scores
// scopes they hold; an owner can mint anything; nil minterScopes (system bootstrap) is uncapped.
func TestAPIKeyScopeCappedToMinter(t *testing.T) {
	pool := mpSetup(t)
	o3clean(t, pool)
	ctx := context.Background()
	seedOrg(t, pool, "org_o3a")
	if _, err := pool.Exec(ctx, `INSERT INTO projects (id, org_id, name) VALUES ('proj_o3a','org_o3a','p') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	member := perm.RoleScopes(perm.RoleMember)
	owner := perm.RoleScopes(perm.RoleOwner)
	viewer := perm.RoleScopes(perm.RoleViewer)

	// A member CANNOT mint the escalating scopes (no traces:write / traces:delete in role).
	for _, sc := range []string{"ingest", "delete"} {
		if _, _, err := CreateAPIKey(ctx, pool, "proj_o3a", []string{sc}, "", member); err != ErrScopeExceedsMinter {
			t.Fatalf("member minting %q: got %v, want ErrScopeExceedsMinter", sc, err)
		}
	}
	// A member CAN mint the scopes their role holds.
	for _, sc := range []string{"query", "query:payloads", "scores:write"} {
		if _, _, err := CreateAPIKey(ctx, pool, "proj_o3a", []string{sc}, "", member); err != nil {
			t.Fatalf("member minting %q (held) must succeed: %v", sc, err)
		}
	}
	// A viewer can mint a read-only query key (holds it) but not payloads.
	if _, _, err := CreateAPIKey(ctx, pool, "proj_o3a", []string{"query"}, "", viewer); err != nil {
		t.Fatalf("viewer minting query (held) must succeed: %v", err)
	}
	if _, _, err := CreateAPIKey(ctx, pool, "proj_o3a", []string{"query:payloads"}, "", viewer); err != ErrScopeExceedsMinter {
		t.Fatalf("viewer minting query:payloads: got %v, want ErrScopeExceedsMinter", err)
	}
	// An owner (holds All()) can mint even the escalating scopes.
	for _, sc := range []string{"ingest", "delete", "query", "scores:write"} {
		if _, _, err := CreateAPIKey(ctx, pool, "proj_o3a", []string{sc}, "", owner); err != nil {
			t.Fatalf("owner minting %q must succeed: %v", sc, err)
		}
	}
	// A mixed request where ONE scope exceeds the minter is rejected whole (no partial mint).
	if _, _, err := CreateAPIKey(ctx, pool, "proj_o3a", []string{"query", "delete"}, "", member); err != ErrScopeExceedsMinter {
		t.Fatalf("member minting [query,delete]: got %v, want ErrScopeExceedsMinter", err)
	}
}
