package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
)

func o4clean(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`DELETE FROM revocations WHERE principal_id LIKE 'o4-%' OR principal_id LIKE 'usr_o4%' OR principal_id LIKE 'plug_o4%'`,
		`DELETE FROM api_keys WHERE project_id LIKE 'proj_o4%'`,
		`DELETE FROM org_memberships WHERE org_id LIKE 'org_o4%' OR user_id LIKE 'usr_o4%'`,
		`DELETE FROM projects WHERE id LIKE 'proj_o4%' OR org_id LIKE 'org_o4%'`,
		`DELETE FROM sessions WHERE user_id LIKE 'usr_o4%'`,
		`DELETE FROM users WHERE id LIKE 'usr_o4%' OR lower(email) LIKE 'o4-%'`,
		`DELETE FROM organizations WHERE id LIKE 'org_o4%'`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("clean: %v", err)
		}
	}
}

// TestRevokedAPIKeyDeniedNextRequest: a revoked API key is denied on the very NEXT
// Authenticate call — same in-process sequence, no TTL wait. (API keys have no TTL; this is
// the row-delete immediacy the #63 requirement demands for the key credential.)
func TestRevokedAPIKeyDeniedNextRequest(t *testing.T) {
	pool := mpSetup(t)
	o4clean(t, pool)
	ctx := context.Background()
	seedOrg(t, pool, "org_o4a")
	if _, err := pool.Exec(ctx, `INSERT INTO projects (id,org_id,name) VALUES ('proj_o4a','org_o4a','p') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	secret, pub, err := CreateAPIKey(ctx, pool, "proj_o4a", []string{"query"}, "", perm.RoleScopes(perm.RoleOwner))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Authenticate(ctx, pool, secret); err != nil {
		t.Fatalf("fresh key must authenticate: %v", err)
	}
	if err := RevokeAPIKey(ctx, pool, "proj_o4a", pub); err != nil {
		t.Fatal(err)
	}
	if _, err := Authenticate(ctx, pool, secret); err == nil {
		t.Fatal("a revoked key must be denied on the NEXT request, not after any TTL")
	}
}

// TestRevokeUserCascade is the load-bearing #63 proof: revoking a user denies EVERY credential
// in the derivation subtree immediately — session, minted API key, and (via the epoch) the
// still-live plugin frontend token / identity assertion (checked here through TokenLive) — and
// blocks re-login. A single surviving credential would be the gap.
func TestRevokeUserCascade(t *testing.T) {
	pool := mpSetup(t)
	o4clean(t, pool)
	ctx := context.Background()
	seedOrg(t, pool, "org_o4a")
	if _, err := pool.Exec(ctx, `INSERT INTO projects (id,org_id,name) VALUES ('proj_o4a','org_o4a','p') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	// A real user with a known password + membership (invite creates the account).
	uid, _, err := InviteMember(ctx, pool, "org_o4a", "o4-victim@t", perm.RoleAdmin, "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	// Derived credentials: a session, and an API key the user minted.
	sess, err := CreateSession(ctx, pool, User{ID: uid, Email: "o4-victim@t"})
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := CreateAPIKey(ctx, pool, "proj_o4a", []string{"query"}, uid, perm.RoleScopes(perm.RoleAdmin))
	if err != nil {
		t.Fatal(err)
	}
	// A frontend token / assertion is stateless; model its liveness via TokenLive with an
	// issued-at BEFORE the revoke (as a real live token would have).
	tokenIat := time.Now().Add(-1 * time.Minute)

	// Before revoke: everything is live.
	if _, err := ResolveSession(ctx, pool, sess.Token); err != nil {
		t.Fatalf("session live before revoke: %v", err)
	}
	if _, err := Authenticate(ctx, pool, secret); err != nil {
		t.Fatalf("key live before revoke: %v", err)
	}
	if live, _ := TokenLive(ctx, pool, "jti-x", "acme/dash", "o4-victim@t", tokenIat); !live {
		t.Fatal("plugin token live before revoke")
	}
	if _, err := VerifyPassword(ctx, pool, "o4-victim@t", "correct-horse-battery"); err != nil {
		t.Fatalf("login works before revoke: %v", err)
	}

	// REVOKE.
	if err := RevokeUser(ctx, pool, uid, "compromised"); err != nil {
		t.Fatalf("revoke user: %v", err)
	}

	// After revoke: the WHOLE subtree is denied, immediately.
	if _, err := ResolveSession(ctx, pool, sess.Token); err == nil {
		t.Error("session must be dead after user revoke")
	}
	if _, err := Authenticate(ctx, pool, secret); err == nil {
		t.Error("minted API key must be dead after user revoke")
	}
	if live, _ := TokenLive(ctx, pool, "jti-x", "acme/dash", "o4-victim@t", tokenIat); live {
		t.Error("still-within-TTL frontend token/assertion must be denied after user revoke (the #63 crown)")
	}
	if revoked, _ := UserRevoked(ctx, pool, "o4-victim@t"); !revoked {
		t.Error("user must read as revoked")
	}
	if _, err := VerifyPassword(ctx, pool, "o4-victim@t", "correct-horse-battery"); err != ErrBadCredentials {
		t.Errorf("re-login must be blocked (ErrBadCredentials), got %v", err)
	}

	// A token minted AFTER the revoke instant (e.g. a different, non-revoked user, or after
	// reinstatement) is unaffected — the epoch denies only what was issued at/before revoke.
	if live, _ := TokenLive(ctx, pool, "jti-y", "acme/dash", "o4-other@t", time.Now().Add(time.Minute)); !live {
		t.Error("a token for a different principal issued after the revoke must remain live")
	}
}

// TestRevokePluginTokensEpoch: revoking a plugin denies its service token issued before the
// revoke; a token issued after (re-enable / re-mint) is live. Proves the plugin cascade and
// the epoch boundary.
func TestRevokePluginTokensEpoch(t *testing.T) {
	pool := mpSetup(t)
	o4clean(t, pool)
	ctx := context.Background()
	before := time.Now().Add(-1 * time.Minute)

	if live, _ := TokenLive(ctx, pool, "jti-a", "plug_o4svc", "", before); !live {
		t.Fatal("plugin token live before any revoke")
	}
	if err := RevokePluginTokens(ctx, pool, "plug_o4svc", "disabled"); err != nil {
		t.Fatal(err)
	}
	// A service token issued before the revoke → denied (no user email — plugin is the deriver).
	if live, _ := TokenLive(ctx, pool, "jti-a", "plug_o4svc", "", before); live {
		t.Error("plugin service token issued before revoke must be denied")
	}
	// A token minted after re-enable (issued-at after the revoke) → live.
	if live, _ := TokenLive(ctx, pool, "jti-b", "plug_o4svc", "", time.Now().Add(time.Minute)); !live {
		t.Error("a service token minted after the revoke instant must be live")
	}
	// Reinstate (delete the row) → even a before-dated token is live again.
	if err := Unrevoke(ctx, pool, RevokeKindPlugin, "plug_o4svc"); err != nil {
		t.Fatal(err)
	}
	if live, _ := TokenLive(ctx, pool, "jti-a", "plug_o4svc", "", before); !live {
		t.Error("after reinstatement the plugin token must be live again")
	}
}

// TestJTIRevoke: a single token can be denied by jti without touching its user or plugin.
func TestJTIRevoke(t *testing.T) {
	pool := mpSetup(t)
	o4clean(t, pool)
	ctx := context.Background()
	before := time.Now().Add(-1 * time.Minute)
	if err := Revoke(ctx, pool, RevokeKindJTI, "o4-jti-1", "leaked"); err != nil {
		t.Fatal(err)
	}
	// The revoked jti is denied; a sibling token (different jti, same plugin/user) is not.
	if live, _ := TokenLive(ctx, pool, "o4-jti-1", "acme/dash", "o4-u@t", before); live {
		t.Error("the revoked jti must be denied")
	}
	if live, _ := TokenLive(ctx, pool, "o4-jti-2", "acme/dash", "o4-u@t", before); !live {
		t.Error("a different jti (same plugin/user) must remain live")
	}
}
