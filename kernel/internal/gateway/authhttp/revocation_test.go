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
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

// TestRevokeUserEndpointAuthz proves the revoke-user endpoint is instance-admin gated and
// actually performs the cascade: a non-instance-admin is 403 (nothing revoked), an unknown
// user is 404, and an instance-admin revoke kills the target's session on the next request.
func TestRevokeUserEndpointAuthz(t *testing.T) {
	url := os.Getenv("LLMOBS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set LLMOBS_TEST_DATABASE_URL to run the O4 revoke-user endpoint test")
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
		`DELETE FROM revocations WHERE principal_id LIKE 'o4h-%'`,
		`DELETE FROM sessions WHERE user_id LIKE 'usr_o4h%'`,
		`DELETE FROM org_memberships WHERE org_id LIKE 'org_o4h%' OR user_id LIKE 'usr_o4h%'`,
		`DELETE FROM users WHERE id LIKE 'usr_o4h%' OR lower(email) LIKE 'o4h-%'`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("clean: %v", err)
		}
	}
	// A victim user with a live session.
	if _, err := pool.Exec(ctx, `INSERT INTO users (id,email,password_hash,role) VALUES ('usr_o4h_victim','o4h-victim@t','x','viewer') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	sess, err := controlplane.CreateSession(ctx, pool, controlplane.User{ID: "usr_o4h_victim", Email: "o4h-victim@t"})
	if err != nil {
		t.Fatal(err)
	}

	h := New(pool, nil, false)
	mux := http.NewServeMux()
	h.RegisterRevocation(mux)

	// Inject the instance-admin seam: only usr_o4h_super is an instance admin.
	h.instanceAdminFn = func(_ context.Context, userID string) (bool, error) { return userID == "usr_o4h_super", nil }

	call := func(actor, target string) int {
		r := httptest.NewRequest(http.MethodPost, "/v1alpha1/users/"+target+"/revoke", strings.NewReader(`{}`))
		r.Header.Set(csrfHeader, "csrf-ok")
		r = r.WithContext(context.WithValue(r.Context(), sessionKey,
			controlplane.Session{CSRFToken: "csrf-ok", User: controlplane.User{ID: actor, Email: actor + "@t"}}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec.Code
	}

	// Non-instance-admin → 403, and the victim's session is UNTOUCHED.
	if got := call("usr_o4h_notsuper", "usr_o4h_victim"); got != http.StatusForbidden {
		t.Fatalf("non-instance-admin revoke = %d, want 403", got)
	}
	if _, err := controlplane.ResolveSession(ctx, pool, sess.Token); err != nil {
		t.Fatalf("a denied revoke must not touch the target's session: %v", err)
	}
	// Unknown user → 404.
	if got := call("usr_o4h_super", "usr_o4h_ghost"); got != http.StatusNotFound {
		t.Fatalf("revoke unknown user = %d, want 404", got)
	}
	// Instance-admin → 204, and the victim's session is dead next request.
	if got := call("usr_o4h_super", "usr_o4h_victim"); got != http.StatusNoContent {
		t.Fatalf("instance-admin revoke = %d, want 204", got)
	}
	if _, err := controlplane.ResolveSession(ctx, pool, sess.Token); err == nil {
		t.Fatal("the revoked user's session must be dead on the next request")
	}
}
