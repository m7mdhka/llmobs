package authhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/secretbox"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

// TestSSOConfigAuthz: configuring SSO requires org:manage (owner) in the TARGET org; the
// group→role map is capped strictly-below the configurer (so SSO can't be set to grant owner);
// and disabling local passwords is refused if no local-password owner remains (break-glass).
func TestSSOConfigAuthz(t *testing.T) {
	dburl := os.Getenv("LLMOBS_TEST_DATABASE_URL")
	if dburl == "" {
		t.Skip("set LLMOBS_TEST_DATABASE_URL to run the O5 SSO config authz test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dburl)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, s := range []string{
		`DELETE FROM sso_group_roles WHERE org_id='org_o5cfg'`,
		`DELETE FROM sso_providers WHERE org_id='org_o5cfg'`,
		`DELETE FROM org_memberships WHERE org_id='org_o5cfg'`,
		`DELETE FROM users WHERE id LIKE 'usr_o5cfg%'`,
		`DELETE FROM organizations WHERE id='org_o5cfg'`,
	} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("clean: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id,name) VALUES ('org_o5cfg','cfg')`); err != nil {
		t.Fatal(err)
	}
	// An owner (with a local password) and an admin in the org.
	mustUser := func(id, email string) {
		if _, err := pool.Exec(ctx, `INSERT INTO users (id,email,password_hash,role) VALUES ($1,$2,'x','viewer') ON CONFLICT DO NOTHING`, id, email); err != nil {
			t.Fatal(err)
		}
	}
	mustUser("usr_o5cfg_owner", "o5cfg-owner@t")
	mustUser("usr_o5cfg_admin", "o5cfg-admin@t")
	_ = controlplane.SetMembership(ctx, pool, "usr_o5cfg_owner", "org_o5cfg", "owner")
	_ = controlplane.SetMembership(ctx, pool, "usr_o5cfg_admin", "org_o5cfg", "admin")

	box, _ := secretbox.NewRandom()
	h := New(pool, nil, false)
	mux := http.NewServeMux()
	h.RegisterSSO(mux, box, http.DefaultClient, "")

	put := func(actor, body string) int {
		r := httptest.NewRequest(http.MethodPut, "/v1alpha1/sso/org_o5cfg", strings.NewReader(body))
		r.Header.Set(csrfHeader, "csrf-ok")
		r = r.WithContext(context.WithValue(r.Context(), sessionKey,
			controlplane.Session{CSRFToken: "csrf-ok", User: controlplane.User{ID: actor, Email: actor + "@t"}}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec.Code
	}
	valid := `{"issuer":"https://idp.example.com","client_id":"c","client_secret":"s","group_roles":{"eng":"member"}}`

	// An admin (no org:manage) cannot configure SSO.
	if got := put("usr_o5cfg_admin", valid); got != http.StatusForbidden {
		t.Fatalf("admin configuring SSO = %d, want 403", got)
	}
	// An owner CAN, mapping to a strictly-below role.
	if got := put("usr_o5cfg_owner", valid); got != http.StatusNoContent {
		t.Fatalf("owner configuring SSO = %d, want 204", got)
	}
	// The strictly-below cap: an owner cannot map a group to owner (at their own level).
	if got := put("usr_o5cfg_owner", `{"issuer":"https://i","client_id":"c","client_secret":"s","group_roles":{"supers":"owner"}}`); got != http.StatusForbidden {
		t.Fatalf("mapping a group to owner = %d, want 403", got)
	}
	// Break-glass: disabling local passwords is allowed here because a local-password owner
	// exists (usr_o5cfg_owner has password_hash='x').
	if got := put("usr_o5cfg_owner", `{"issuer":"https://i","client_id":"c","client_secret":"s","local_password_disabled":true,"group_roles":{"eng":"member"}}`); got != http.StatusNoContent {
		t.Fatalf("disable-local-password WITH a break-glass owner = %d, want 204", got)
	}
	// Remove the local password from the sole owner → disabling must now be refused (409).
	if _, err := pool.Exec(ctx, `UPDATE users SET password_hash=NULL WHERE id='usr_o5cfg_owner'`); err != nil {
		t.Fatal(err)
	}
	if got := put("usr_o5cfg_owner", `{"issuer":"https://i","client_id":"c","client_secret":"s","local_password_disabled":true,"group_roles":{"eng":"member"}}`); got != http.StatusConflict {
		t.Fatalf("disable-local-password with NO break-glass owner = %d, want 409", got)
	}
}
