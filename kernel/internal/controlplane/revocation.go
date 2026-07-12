package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Immediate credential revocation. The invariant: a derived credential must
// never OUTLIVE its deriver — revoking a principal denies every credential in its derivation
// subtree on the VERY NEXT request, before TTL.
//
// Two mechanisms, one authority:
//   - Sessions and API keys are DB-looked-up every request → revoked by row DELETE (immediate
//     by construction; see RevokeUser's cascade and RevokeAPIKey).
//   - Kernel-signed plugin tokens are STATELESS (signature + TTL only) → revoked via this
//     epoch store, consulted at the ONE signed-token verify chokepoint (plugintoken.Signer,
//     which every plugin-token path funnels through). TokenLive is that consult.

// Revocation principal kinds. A signed plugin token carries the deriving user's email (Sub),
// the plugin id (service token / assertion audience), and its own jti — the three keys a
// verify can check without any token-format change.
const (
	RevokeKindUser   = "user"   // principal_id = lower(email)
	RevokeKindPlugin = "plugin" // principal_id = plugin id
	RevokeKindJTI    = "jti"    // principal_id = token jti (targeted single-token revoke)
)

// Revoke records a principal as revoked as of now(). Idempotent (re-revoking refreshes the
// instant). A permanent authorization decision — the caller is the gate.
func Revoke(ctx context.Context, pool *pgxpool.Pool, kind, id, reason string) error {
	if kind == RevokeKindUser {
		id = strings.ToLower(strings.TrimSpace(id))
	}
	if id == "" {
		return fmt.Errorf("revoke: empty principal id for kind %q", kind)
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO revocations (principal_kind, principal_id, reason) VALUES ($1,$2,$3)
		 ON CONFLICT (principal_kind, principal_id) DO UPDATE SET revoked_at = now(), reason = EXCLUDED.reason`,
		kind, id, reason)
	return err
}

// Unrevoke removes a revocation (reinstates a principal). Not wired to a user-facing endpoint
// (only the supervisor re-enable path uses it), but the epoch model supports it: a token
// minted AFTER the row is gone verifies normally.
func Unrevoke(ctx context.Context, pool *pgxpool.Pool, kind, id string) error {
	if kind == RevokeKindUser {
		id = strings.ToLower(strings.TrimSpace(id))
	}
	_, err := pool.Exec(ctx, `DELETE FROM revocations WHERE principal_kind=$1 AND principal_id=$2`, kind, id)
	return err
}

// TokenLive reports whether a stateless plugin token is still valid — i.e. NEITHER its jti,
// NOR its plugin, NOR its deriving user (by email) has been revoked at or after the token was
// issued. This is the OUTLIVE half of the derived-credential invariant, checked at every
// signed-token verify. Any of the three principals revoked with revoked_at >= issuedAt →
// not live → deny on the next request (the token was issued before the revoke instant, so a
// still-within-TTL but revoked credential is denied — the crown case immediate revocation
// exists to close).
//
// userEmail is "" for a service token (no deriving user — its deriver is the plugin). Empty
// jti/pluginID/email simply match nothing. The caller (plugintoken.Signer) FAILS CLOSED on a
// non-nil error: a revocation check that cannot run must not silently admit the token.
func TokenLive(ctx context.Context, pool *pgxpool.Pool, jti, pluginID, userEmail string, issuedAt time.Time) (bool, error) {
	var one int
	err := pool.QueryRow(ctx,
		`SELECT 1 FROM revocations
		  WHERE revoked_at >= $4
		    AND ( (principal_kind='jti'    AND principal_id=$1)
		       OR (principal_kind='plugin' AND principal_id=$2)
		       OR (principal_kind='user'   AND principal_id=$3) )
		  LIMIT 1`,
		jti, pluginID, strings.ToLower(userEmail), issuedAt).Scan(&one)
	if err != nil {
		if err == pgx.ErrNoRows {
			return true, nil // no matching revocation → live
		}
		return false, err // fail closed at the caller
	}
	return false, nil // a revocation matched → not live
}

// UserRevoked reports whether a user (by email) is currently revoked — used to block LOGIN of
// a revoked account (a revoked user must not be able to mint a fresh session and start the
// derivation tree over). Existence check, no issued-at (login has no token).
func UserRevoked(ctx context.Context, pool *pgxpool.Pool, email string) (bool, error) {
	var one int
	err := pool.QueryRow(ctx,
		`SELECT 1 FROM revocations WHERE principal_kind=$1 AND principal_id=$2 LIMIT 1`,
		RevokeKindUser, strings.ToLower(strings.TrimSpace(email))).Scan(&one)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// RevokeUser is the CASCADE — the load-bearing correctness of immediate revocation. Revoking
// a user denies EVERY credential in their derivation subtree, immediately and atomically:
//   - sessions: DELETE → the cookie is dead next request (ResolveSession re-queries).
//   - API keys they minted (created_by_user_id): DELETE → the bearer fails next lookup.
//   - a user-epoch revocation: their still-live frontend tokens AND identity assertions
//     (which carry their email) are denied at the signed-token verify (TokenLive).
//   - login: blocked while the revocation row exists (UserRevoked), so the tree can't restart.
//
// One transaction: a partial cascade (e.g. sessions gone but keys or the epoch left) would be
// exactly the gap this closes — a surviving derived credential — so all four commit together.
func RevokeUser(ctx context.Context, pool *pgxpool.Pool, userID, reason string) error {
	var email string
	if err := pool.QueryRow(ctx, `SELECT email FROM users WHERE id=$1`, userID).Scan(&email); err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("revoke user: unknown user %q", userID)
		}
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID); err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM api_keys WHERE created_by_user_id=$1`, userID); err != nil {
		return fmt.Errorf("revoke user api keys: %w", err)
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO revocations (principal_kind, principal_id, reason) VALUES ($1,$2,$3)
		 ON CONFLICT (principal_kind, principal_id) DO UPDATE SET revoked_at = now(), reason = EXCLUDED.reason`,
		RevokeKindUser, strings.ToLower(email), reason); err != nil {
		return fmt.Errorf("revoke user epoch: %w", err)
	}
	return tx.Commit(ctx)
}

// RevokePluginTokens denies a plugin's still-live kernel-signed tokens (its service token, and
// any identity/frontend token issued for it) immediately — the "revoked plugin's token denied
// mid-session" case. Wired to the supervisor's disable/uninstall so a stopped plugin's
// last-minted service token (TTL up to 10m) cannot keep calling kernel APIs until it expires.
func RevokePluginTokens(ctx context.Context, pool *pgxpool.Pool, pluginID, reason string) error {
	return Revoke(ctx, pool, RevokeKindPlugin, pluginID, reason)
}
