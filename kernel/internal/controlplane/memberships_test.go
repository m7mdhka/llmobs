package controlplane

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

// mpSetup connects to the test Postgres, migrates, and returns a clean pool. These are the
// RBAC-foundation prove-the-negatives (Arc O / O1) on the real schema.
func mpSetup(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("LLMOBS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set LLMOBS_TEST_DATABASE_URL to run the RBAC membership tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Clean this test's rows (shared DB).
	for _, stmt := range []string{
		`DELETE FROM org_memberships WHERE user_id LIKE 'usr_o1test%'`,
		`DELETE FROM sessions WHERE user_id LIKE 'usr_o1test%'`,
		`DELETE FROM users WHERE id LIKE 'usr_o1test%'`,
		`DELETE FROM projects WHERE org_id IN ('org_o1a','org_o1b')`,
		`DELETE FROM organizations WHERE id IN ('org_o1a','org_o1b')`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("clean: %v", err)
		}
	}
	return pool
}

func seedOrg(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name) VALUES ($1,$1) ON CONFLICT DO NOTHING`, id); err != nil {
		t.Fatalf("seed org %s: %v", id, err)
	}
}

func seedUser(t *testing.T, pool *pgxpool.Pool, id, email string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, role) VALUES ($1,$2,'x','viewer') ON CONFLICT DO NOTHING`,
		id, email); err != nil {
		t.Fatalf("seed user %s: %v", id, err)
	}
}

// TestMembershipIsPerOrg: a user's authority is per-org. Owner in org A has zero scope in
// org B they don't belong to (the cross-org prove-the-negative).
func TestMembershipIsPerOrg(t *testing.T) {
	pool := mpSetup(t)
	ctx := context.Background()
	seedOrg(t, pool, "org_o1a")
	seedOrg(t, pool, "org_o1b")
	seedUser(t, pool, "usr_o1test_a", "a@o1.test")

	if err := SetMembership(ctx, pool, "usr_o1test_a", "org_o1a", perm.RoleOwner); err != nil {
		t.Fatalf("set membership: %v", err)
	}

	// In org A: owner → full scopes.
	roleA, _ := RoleInOrg(ctx, pool, "usr_o1test_a", "org_o1a")
	if roleA != perm.RoleOwner {
		t.Fatalf("role in org A = %q, want owner", roleA)
	}
	if !perm.HasWriteAuthority(perm.RoleScopes(roleA)) || !perm.Has(perm.RoleScopes(roleA), perm.OrgManage) {
		t.Fatal("owner in org A must have full authority")
	}

	// In org B: no membership → "" → NO scopes. Prove-the-negative.
	roleB, _ := RoleInOrg(ctx, pool, "usr_o1test_a", "org_o1b")
	if roleB != "" {
		t.Fatalf("role in org B = %q, want empty (not a member)", roleB)
	}
	if s := perm.RoleScopes(roleB); len(s) != 0 {
		t.Fatalf("non-member must have NO scopes in org B, got %v", s)
	}
}

// TestSetMembershipRejectsInvalidRole: only defined roles are persistable.
func TestSetMembershipRejectsInvalidRole(t *testing.T) {
	pool := mpSetup(t)
	ctx := context.Background()
	seedOrg(t, pool, "org_o1a")
	seedUser(t, pool, "usr_o1test_x", "x@o1.test")
	if err := SetMembership(ctx, pool, "usr_o1test_x", "org_o1a", "superuser"); err == nil {
		t.Fatal("SetMembership must reject an undefined role")
	}
}

// TestSessionResolvesMembershipRole: the session's role is the SERVER-RESOLVED membership
// role in the default org — not the legacy users.role, and not any client input.
func TestSessionResolvesMembershipRole(t *testing.T) {
	pool := mpSetup(t)
	ctx := context.Background()
	// The default org is the earliest project's org; seed one so it's deterministic.
	seedOrg(t, pool, "org_o1a")
	if _, err := pool.Exec(ctx, `INSERT INTO projects (id, org_id, name) VALUES ('proj_o1a','org_o1a','p') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	// Note: the shared DB may already carry an earlier default project (e.g. proj_default).
	// Resolve whichever org DefaultOrgID picks and grant the membership THERE, so the test
	// asserts the session-resolution logic regardless of which project sorts first.
	defOrg, err := DefaultOrgID(ctx, pool)
	if err != nil {
		t.Fatalf("default org: %v", err)
	}
	seedUser(t, pool, "usr_o1test_s", "s@o1.test")
	// users.role is 'viewer' (seed), but the membership says member — the membership must win.
	if err := SetMembership(ctx, pool, "usr_o1test_s", defOrg, perm.RoleMember); err != nil {
		t.Fatalf("set membership: %v", err)
	}
	sess, err := CreateSession(ctx, pool, User{ID: "usr_o1test_s", Email: "s@o1.test"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	resolved, err := ResolveSession(ctx, pool, sess.Token)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	if resolved.User.Role != perm.RoleMember {
		t.Fatalf("resolved session role = %q, want member (the membership, not users.role)", resolved.User.Role)
	}
	// A member cannot write config (HasWriteAuthority) but can read+score.
	scopes := perm.RoleScopes(resolved.User.Role)
	if perm.Has(scopes, perm.MembersManage) {
		t.Fatal("member session must NOT hold members:manage")
	}
	if !perm.Has(scopes, perm.ScoresWrite) {
		t.Fatal("member session must hold scores:write")
	}
}
