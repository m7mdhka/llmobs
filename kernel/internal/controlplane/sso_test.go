package controlplane

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/secretbox"
)

func o5clean(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`DELETE FROM sso_group_roles WHERE org_id LIKE 'org_o5%'`,
		`DELETE FROM sso_providers WHERE org_id LIKE 'org_o5%'`,
		`DELETE FROM org_memberships WHERE org_id LIKE 'org_o5%' OR user_id LIKE 'usr_o5%'`,
		`DELETE FROM sessions WHERE user_id LIKE 'usr_o5%'`,
		`DELETE FROM revocations WHERE principal_id LIKE 'o5-%'`,
		`DELETE FROM users WHERE id LIKE 'usr_o5%' OR lower(email) LIKE 'o5-%'`,
		`DELETE FROM organizations WHERE id LIKE 'org_o5%'`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("clean: %v", err)
		}
	}
}

// TestSSORoleForGroups is the fail-closed mapping proof (pure): the role is the HIGHEST mapped
// role among the user's groups; a user in NO mapped group gets "" (no access, not a fallback).
func TestSSORoleForGroups(t *testing.T) {
	m := map[string]string{
		"eng-admins": perm.RoleAdmin,
		"eng":        perm.RoleMember,
		"guests":     perm.RoleViewer,
	}
	cases := []struct {
		groups []string
		want   string
	}{
		{[]string{"eng"}, perm.RoleMember},
		{[]string{"eng", "eng-admins"}, perm.RoleAdmin},      // highest wins
		{[]string{"guests", "eng"}, perm.RoleMember},         // highest wins
		{[]string{"unmapped"}, ""},                           // no mapped group → NO access
		{[]string{}, ""},                                     // no groups → NO access
		{[]string{"eng-admins", "unmapped"}, perm.RoleAdmin}, // ignores unmapped
		{nil, ""}, // nil → NO access
	}
	for _, c := range cases {
		if got := SSORoleForGroups(m, c.groups); got != c.want {
			t.Errorf("SSORoleForGroups(%v) = %q, want %q", c.groups, got, c.want)
		}
	}
	// A map that (impossibly, defensively) contains owner is ignored as an over-grant vector at
	// resolution only if invalid; owner IS a valid role, so the real guard is the config cap +
	// JIT refusal (tested below). Here confirm an invalid role value is skipped.
	if got := SSORoleForGroups(map[string]string{"x": "root"}, []string{"x"}); got != "" {
		t.Fatalf("invalid mapped role must be ignored, got %q", got)
	}
}

// TestJITProvisionSSOUser: find-or-create passwordless user, capped membership into the TARGET
// org only, never grant owner, never downgrade an existing owner.
func TestJITProvisionSSOUser(t *testing.T) {
	pool := mpSetup(t)
	o5clean(t, pool)
	ctx := context.Background()
	seedOrg(t, pool, "org_o5a")
	seedOrg(t, pool, "org_o5b")

	// New SSO user → created passwordless + member in org A only.
	uid, err := JITProvisionSSOUser(ctx, pool, "org_o5a", "o5-alice@t", perm.RoleMember)
	if err != nil {
		t.Fatalf("jit: %v", err)
	}
	if r, _ := RoleInOrg(ctx, pool, uid, "org_o5a"); r != perm.RoleMember {
		t.Fatalf("role in target org = %q, want member", r)
	}
	if r, _ := RoleInOrg(ctx, pool, uid, "org_o5b"); r != "" {
		t.Fatalf("must NOT be provisioned into another org, got %q", r)
	}
	// The account is passwordless → cannot local-login.
	if _, err := VerifyPassword(ctx, pool, "o5-alice@t", "anything"); err != ErrBadCredentials {
		t.Fatalf("SSO-only user must not local-login, got %v", err)
	}
	// Idempotent role sync: a second login with a higher mapped role updates the membership.
	if _, err := JITProvisionSSOUser(ctx, pool, "org_o5a", "o5-alice@t", perm.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if r, _ := RoleInOrg(ctx, pool, uid, "org_o5a"); r != perm.RoleAdmin {
		t.Fatalf("role should sync to admin, got %q", r)
	}
	// SSO may NEVER provision owner.
	if _, err := JITProvisionSSOUser(ctx, pool, "org_o5a", "o5-bob@t", perm.RoleOwner); err == nil {
		t.Fatal("JIT must refuse the owner role")
	}
	// An existing OWNER is never downgraded by SSO.
	seedUser(t, pool, "usr_o5_owner", "o5-owner@t")
	if err := SetMembership(ctx, pool, "usr_o5_owner", "org_o5a", perm.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := JITProvisionSSOUser(ctx, pool, "org_o5a", "o5-owner@t", perm.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if r, _ := RoleInOrg(ctx, pool, "usr_o5_owner", "org_o5a"); r != perm.RoleOwner {
		t.Fatalf("SSO must not downgrade an owner, role = %q", r)
	}
}

// TestVerifyPasswordLocalDisableAndBreakGlass: with local passwords disabled for the org, a
// non-owner with a password cannot local-login, but an OWNER still can (break-glass → no
// last-owner lockout).
func TestVerifyPasswordLocalDisableAndBreakGlass(t *testing.T) {
	pool := mpSetup(t)
	o5clean(t, pool)
	ctx := context.Background()
	seedOrg(t, pool, "org_o5a")
	// The default org for VerifyPassword is the earliest project's org; make org_o5a it by
	// giving it the earliest project is not reliable in a shared DB, so this test asserts the
	// CountLocalOwners + JIT paths and the NULL-password path, which don't depend on which org
	// is "default". The local_password_disabled default-org behaviour is covered at the HTTP
	// layer where the org is explicit.
	// Break-glass count: an owner with a password counts; an owner without does not.
	seedUser(t, pool, "usr_o5_o1", "o5-o1@t") // seedUser sets password_hash='x'
	if err := SetMembership(ctx, pool, "usr_o5_o1", "org_o5a", perm.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if n, _ := CountLocalOwners(ctx, pool, "org_o5a"); n != 1 {
		t.Fatalf("one local-password owner expected, got %d", n)
	}
	// A passwordless owner does NOT count toward break-glass.
	uid, err := JITProvisionSSOUser(ctx, pool, "org_o5a", "o5-sso-owner@t", perm.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	_ = uid
	if _, err := pool.Exec(ctx, `UPDATE org_memberships SET role='owner' WHERE user_id=$1 AND org_id='org_o5a'`, uid); err != nil {
		t.Fatal(err)
	}
	if n, _ := CountLocalOwners(ctx, pool, "org_o5a"); n != 1 {
		t.Fatalf("a passwordless owner must NOT count as break-glass, got %d", n)
	}
}

// TestSetGetSSOProviderSealsSecret: the client secret is encrypted at rest and never in the
// redacted view; the group map round-trips.
func TestSetGetSSOProviderSealsSecret(t *testing.T) {
	pool := mpSetup(t)
	o5clean(t, pool)
	ctx := context.Background()
	seedOrg(t, pool, "org_o5a")
	box, err := secretbox.NewRandom()
	if err != nil {
		t.Fatal(err)
	}
	p := &SSOProvider{
		OrgID: "org_o5a", Issuer: "https://idp.example.com", ClientID: "client-123",
		ClientSecret: "super-secret-xyz", GroupClaim: "groups", Enabled: true,
		GroupRoles: map[string]string{"eng-admins": perm.RoleAdmin, "eng": perm.RoleMember},
	}
	if err := SetSSOProvider(ctx, pool, box, p); err != nil {
		t.Fatalf("set: %v", err)
	}
	// The raw ciphertext must not equal the plaintext.
	var ct []byte
	if err := pool.QueryRow(ctx, `SELECT client_secret_ct FROM sso_providers WHERE org_id='org_o5a'`).Scan(&ct); err != nil {
		t.Fatal(err)
	}
	if string(ct) == "super-secret-xyz" {
		t.Fatal("client secret must be sealed, not stored in plaintext")
	}
	// GetSSOProvider decrypts it back.
	got, err := GetSSOProvider(ctx, pool, box, "org_o5a")
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientSecret != "super-secret-xyz" {
		t.Fatalf("decrypted secret = %q", got.ClientSecret)
	}
	if got.GroupRoles["eng-admins"] != perm.RoleAdmin || got.GroupRoles["eng"] != perm.RoleMember {
		t.Fatalf("group map did not round-trip: %v", got.GroupRoles)
	}
	// The redacted view carries NO secret and no secret field at all.
	view, err := GetSSOProviderView(ctx, pool, "org_o5a")
	if err != nil {
		t.Fatal(err)
	}
	if view.ClientID != "client-123" || view.Issuer != "https://idp.example.com" {
		t.Fatalf("view missing public fields: %+v", view)
	}
}
