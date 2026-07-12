package authhttp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

// The exhaustive provisioning escalation matrix at the HTTP boundary, on the
// real schema. It proves, per sibling: no principal assigns/affects a role at or above their
// own; no sibling is reachable without the shared members:manage gate; the gate is resolved
// against the TARGET org (cross-org is blocked); the SERVER session governs (never the body);
// owners are immutable (structural last-owner protection); a non-member target is 404.

func provSetup(t *testing.T) (*Handler, *http.ServeMux, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("LLMOBS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set LLMOBS_TEST_DATABASE_URL to run the O3 provisioning matrix")
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
	for _, stmt := range []string{
		`DELETE FROM org_memberships WHERE org_id LIKE 'org_o3h%' OR user_id LIKE 'usr_o3h%'`,
		`DELETE FROM projects WHERE org_id LIKE 'org_o3h%'`,
		`DELETE FROM users WHERE id LIKE 'usr_o3h%' OR lower(email) LIKE 'o3h-%'`,
		`DELETE FROM organizations WHERE id LIKE 'org_o3h%'`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("clean: %v", err)
		}
	}
	h := New(pool, nil, false)
	mux := http.NewServeMux()
	h.RegisterProvisioning(mux)
	return h, mux, pool
}

func provSeedOrg(t *testing.T, pool *pgxpool.Pool, org string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO organizations (id,name) VALUES ($1,$1) ON CONFLICT DO NOTHING`, org); err != nil {
		t.Fatalf("seed org: %v", err)
	}
}

func provSeedMember(t *testing.T, pool *pgxpool.Pool, uid, org, role string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id,email,password_hash,role) VALUES ($1,$2,'x','viewer') ON CONFLICT DO NOTHING`, uid, uid+"@o3h.t"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := controlplane.SetMembership(ctx, pool, uid, org, role); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}

// asActor injects a session for userID with a valid CSRF token (state-changing methods need
// it). The actor is the SERVER session — there is no body field a caller could use to claim a
// different actor; this is what "server-resolved actor governs" means in practice.
func asActor(r *http.Request, userID string) *http.Request {
	sess := controlplane.Session{CSRFToken: "csrf-ok", User: controlplane.User{ID: userID, Email: userID + "@o3h.t"}}
	r.Header.Set(csrfHeader, "csrf-ok")
	return r.WithContext(context.WithValue(r.Context(), sessionKey, sess))
}

func do(mux *http.ServeMux, method, path, actor, body string) *httptest.ResponseRecorder {
	r := asActor(httptest.NewRequest(method, path, strings.NewReader(body)), actor)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// TestInviteEscalationMatrix: the crown jewel for invite — an actor may invite only roles
// STRICTLY below their own, and only if they hold members:manage in the TARGET org.
func TestInviteEscalationMatrix(t *testing.T) {
	_, mux, pool := provSetup(t)
	provSeedOrg(t, pool, "org_o3hx")
	provSeedOrg(t, pool, "org_o3hy") // a second org the admin is NOT a member of
	provSeedMember(t, pool, "usr_o3h_owner", "org_o3hx", perm.RoleOwner)
	provSeedMember(t, pool, "usr_o3h_admin", "org_o3hx", perm.RoleAdmin)
	provSeedMember(t, pool, "usr_o3h_member", "org_o3hx", perm.RoleMember)
	provSeedMember(t, pool, "usr_o3h_viewer", "org_o3hx", perm.RoleViewer)
	provSeedMember(t, pool, "usr_o3h_yowner", "org_o3hy", perm.RoleOwner)

	path := "/v1alpha1/orgs/org_o3hx/members"
	n := 0
	invite := func(actor, role string) int {
		n++
		email := fmt.Sprintf("o3h-invitee-%d@t", n)
		body := fmt.Sprintf(`{"email":%q,"role":%q,"initial_password":"pw-long-enough"}`, email, role)
		return do(mux, http.MethodPost, path, actor, body).Code
	}
	cases := []struct {
		actor, role string
		want        int
	}{
		// admin (members:manage) assigns STRICTLY below → ok; at/above → forbidden.
		{"usr_o3h_admin", perm.RoleViewer, 201},
		{"usr_o3h_admin", perm.RoleMember, 201},
		{"usr_o3h_admin", perm.RoleAdmin, 403}, // at own level — no lateral clone
		{"usr_o3h_admin", perm.RoleOwner, 403}, // above
		// owner assigns admin/member/viewer; owner→owner forbidden (peer).
		{"usr_o3h_owner", perm.RoleAdmin, 201},
		{"usr_o3h_owner", perm.RoleOwner, 403},
		// member/viewer have no members:manage → forbidden regardless of assigned role.
		{"usr_o3h_member", perm.RoleViewer, 403},
		{"usr_o3h_viewer", perm.RoleViewer, 403},
	}
	for _, c := range cases {
		if got := invite(c.actor, c.role); got != c.want {
			t.Errorf("invite by %s of %s = %d, want %d", c.actor, c.role, got, c.want)
		}
	}

	// CROSS-ORG: the org_o3hx admin has NO membership in org_o3hy → cannot invite there,
	// even though they are a full admin in x. The gate resolves against the TARGET org.
	crossBody := `{"email":"o3h-cross@t","role":"viewer","initial_password":"pw-long-enough"}`
	if rec := do(mux, http.MethodPost, "/v1alpha1/orgs/org_o3hy/members", "usr_o3h_admin", crossBody); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-org invite must be 403 (no members:manage in target org), got %d", rec.Code)
	}

	// Invalid role → 400 (before any authority spend is irrelevant; the point is it's rejected).
	if rec := do(mux, http.MethodPost, path, "usr_o3h_admin", `{"email":"o3h-z@t","role":"root","initial_password":"pw-long-enough"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid role must be 400, got %d", rec.Code)
	}
}

// TestSetRoleAndRemoveEscalationMatrix: set-role and remove obey the same strictly-below cap
// on BOTH the target's current role and the new role — an actor cannot touch a peer or a
// superior (so owners are immutable), cannot escalate self or others, and 404s a non-member.
func TestSetRoleAndRemoveEscalationMatrix(t *testing.T) {
	_, mux, pool := provSetup(t)
	provSeedOrg(t, pool, "org_o3hx")
	provSeedMember(t, pool, "usr_o3h_owner", "org_o3hx", perm.RoleOwner)
	provSeedMember(t, pool, "usr_o3h_admin", "org_o3hx", perm.RoleAdmin)
	provSeedMember(t, pool, "usr_o3h_admin2", "org_o3hx", perm.RoleAdmin) // a peer admin
	provSeedMember(t, pool, "usr_o3h_member", "org_o3hx", perm.RoleMember)

	set := func(actor, target, role string) int {
		body := fmt.Sprintf(`{"role":%q}`, role)
		return do(mux, http.MethodPut, "/v1alpha1/orgs/org_o3hx/members/"+target, actor, body).Code
	}
	remove := func(actor, target string) int {
		return do(mux, http.MethodDelete, "/v1alpha1/orgs/org_o3hx/members/"+target, actor, "").Code
	}

	// --- set-role ---
	// admin may retarget a member to viewer (both strictly below admin).
	if got := set("usr_o3h_admin", "usr_o3h_member", perm.RoleViewer); got != 200 {
		t.Errorf("admin set member→viewer = %d, want 200", got)
	}
	// admin cannot promote a member to admin (new role at own level) or owner (above).
	if got := set("usr_o3h_admin", "usr_o3h_member", perm.RoleAdmin); got != 403 {
		t.Errorf("admin set member→admin = %d, want 403", got)
	}
	if got := set("usr_o3h_admin", "usr_o3h_member", perm.RoleOwner); got != 403 {
		t.Errorf("admin set member→owner = %d, want 403", got)
	}
	// admin cannot modify a PEER admin (target at own level).
	if got := set("usr_o3h_admin", "usr_o3h_admin2", perm.RoleViewer); got != 403 {
		t.Errorf("admin set peer-admin = %d, want 403", got)
	}
	// admin cannot modify the OWNER (target above) — the last-owner protection, structural.
	if got := set("usr_o3h_admin", "usr_o3h_owner", perm.RoleViewer); got != 403 {
		t.Errorf("admin set owner→viewer = %d, want 403", got)
	}
	// admin cannot escalate SELF (target at own level).
	if got := set("usr_o3h_admin", "usr_o3h_admin", perm.RoleOwner); got != 403 {
		t.Errorf("admin self-escalate = %d, want 403", got)
	}
	// a member (no members:manage) cannot set any role.
	if got := set("usr_o3h_member", "usr_o3h_member", perm.RoleViewer); got != 403 {
		t.Errorf("member set-role = %d, want 403", got)
	}
	// non-member target → 404.
	if got := set("usr_o3h_admin", "usr_o3h_ghost", perm.RoleViewer); got != 404 {
		t.Errorf("set-role on non-member = %d, want 404", got)
	}

	// --- remove ---
	// admin cannot remove the owner (target above) → owner survives.
	if got := remove("usr_o3h_admin", "usr_o3h_owner"); got != 403 {
		t.Errorf("admin remove owner = %d, want 403", got)
	}
	if n, _ := controlplane.CountOwners(context.Background(), pool, "org_o3hx"); n != 1 {
		t.Fatalf("owner must survive removal attempt, owner count = %d", n)
	}
	// admin cannot remove a peer admin.
	if got := remove("usr_o3h_admin", "usr_o3h_admin2"); got != 403 {
		t.Errorf("admin remove peer-admin = %d, want 403", got)
	}
	// a member cannot remove anyone.
	if got := remove("usr_o3h_member", "usr_o3h_admin2"); got != 403 {
		t.Errorf("member remove = %d, want 403", got)
	}
	// admin CAN remove a strictly-below member → 204, gone.
	if got := remove("usr_o3h_admin", "usr_o3h_member"); got != 204 {
		t.Errorf("admin remove member = %d, want 204", got)
	}
	if r, _ := controlplane.RoleInOrg(context.Background(), pool, "usr_o3h_member", "org_o3hx"); r != "" {
		t.Fatalf("member should be removed, role = %q", r)
	}
	// removing a now-non-member → 404.
	if got := remove("usr_o3h_admin", "usr_o3h_member"); got != 404 {
		t.Errorf("remove non-member = %d, want 404", got)
	}
}

// TestCreateOrgRequiresInstanceAdmin: create-org gates on instance-admin (an owner of the
// default org), the explicit instance-level authority — NOT a per-org role. The creator
// becomes the new org's owner.
func TestCreateOrgRequiresInstanceAdmin(t *testing.T) {
	h, mux, pool := provSetup(t)
	provSeedOrg(t, pool, "org_o3hnil")
	provSeedMember(t, pool, "usr_o3h_super", "org_o3hnil", perm.RoleOwner) // identity for the actor
	// Inject the instance-admin seam so the test does not depend on which project sorts first
	// as the "default org" in the shared DB: only usr_o3h_super is the instance admin.
	h.instanceAdminFn = func(_ context.Context, userID string) (bool, error) {
		return userID == "usr_o3h_super", nil
	}
	provSeedMember(t, pool, "usr_o3h_notsuper", "org_o3hnil", perm.RoleAdmin)

	// Non-instance-admin → 403, nothing created.
	if rec := do(mux, http.MethodPost, "/v1alpha1/orgs", "usr_o3h_notsuper", `{"name":"Nope"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("non-instance-admin create-org must be 403, got %d %s", rec.Code, rec.Body.String())
	}
	// Instance-admin → 201, creator is the new org's owner.
	rec := do(mux, http.MethodPost, "/v1alpha1/orgs", "usr_o3h_super", `{"name":"Fresh O3 Org"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("instance-admin create-org must be 201, got %d %s", rec.Code, rec.Body.String())
	}
	// The created org has exactly one owner: the creator.
	var newOrg string
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM organizations WHERE name = 'Fresh O3 Org' ORDER BY created_at DESC LIMIT 1`).Scan(&newOrg); err != nil {
		t.Fatalf("find created org: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM org_memberships WHERE org_id=$1`, newOrg)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, newOrg)
	})
	if r, _ := controlplane.RoleInOrg(context.Background(), pool, "usr_o3h_super", newOrg); r != perm.RoleOwner {
		t.Fatalf("creator role in new org = %q, want owner", r)
	}
}

// TestProvisioningRequiresCSRFAndSession: every mutating sibling inherits RequireAuth — no
// session is 401, a bad/missing CSRF is 403 — BEFORE any authority is evaluated.
func TestProvisioningRequiresCSRFAndSession(t *testing.T) {
	_, mux, pool := provSetup(t)
	provSeedOrg(t, pool, "org_o3hx")
	provSeedMember(t, pool, "usr_o3h_admin", "org_o3hx", perm.RoleAdmin)

	// No session → 401.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1alpha1/orgs/org_o3hx/members", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session must be 401, got %d", rec.Code)
	}
	// Session but no CSRF header → 403.
	sess := controlplane.Session{CSRFToken: "csrf-ok", User: controlplane.User{ID: "usr_o3h_admin"}}
	r := httptest.NewRequest(http.MethodPost, "/v1alpha1/orgs/org_o3hx/members", strings.NewReader(`{}`)).
		WithContext(context.WithValue(context.Background(), sessionKey, sess))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF must be 403, got %d", rec.Code)
	}
}
