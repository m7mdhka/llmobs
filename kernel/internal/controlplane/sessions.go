package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionTTL is how long a login lasts before re-auth is required.
const SessionTTL = 12 * time.Hour

// Session is a resolved server-side session. Token is only ever returned at
// creation (to set the cookie); afterwards only its hash is known.
type Session struct {
	Token     string // opaque cookie value (creation only)
	CSRFToken string
	User      User
	ExpiresAt time.Time
}

// hashToken is the at-rest form of a session/cookie token (never store the raw).
func hashToken(token string) string {
	sum := sha256.Sum256([]byte("llmobs-session:" + token))
	return hex.EncodeToString(sum[:])
}

// CreateSession mints a new session for a user and returns the raw cookie token
// and CSRF token (persisting only their hashes / the CSRF value).
func CreateSession(ctx context.Context, pool *pgxpool.Pool, u User) (Session, error) {
	token, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	csrf, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	expires := nowUTC().Add(SessionTTL)
	if _, err := pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, csrf_token, expires_at) VALUES ($1,$2,$3,$4)`,
		hashToken(token), u.ID, csrf, expires); err != nil {
		return Session{}, err
	}
	return Session{Token: token, CSRFToken: csrf, User: u, ExpiresAt: expires}, nil
}

// ResolveSession validates a cookie token and returns the session (without the
// raw token). Expired or unknown tokens return ErrUnauthorized.
func ResolveSession(ctx context.Context, pool *pgxpool.Pool, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrUnauthorized
	}
	var s Session
	var expires time.Time
	// The session's role is the user's SERVER-RESOLVED membership role in the active org
	// (the default org's, in the lite single-org profile) — NOT the legacy flat
	// users.role, and never a client-supplied claim. A user with no membership resolves to
	// role "" → perm.RoleScopes returns no scopes (fail closed). O2 wires per-project-org
	// resolution; O1 resolves the default org here (OrgForProject/RoleInOrg exist for it).
	//
	// COUPLING (O5): the org this resolves authority from (the earliest project's org) MUST stay
	// equal to controlplane.DefaultOrgID — SSO's single-login-org guard (authhttp isLoginOrg)
	// relies on it, so an SSO login can only provision into the org whose authority a session
	// resolves here. If this ever becomes org-scoped (multi-org login), revisit the SSO guard in
	// lockstep or the cross-org escalation reopens.
	err := pool.QueryRow(ctx,
		`SELECT s.csrf_token, s.expires_at, u.id, u.email, COALESCE(m.role, '')
		   FROM sessions s
		   JOIN users u ON u.id = s.user_id
		   LEFT JOIN org_memberships m
		     ON m.user_id = u.id
		    AND m.org_id = (SELECT org_id FROM projects ORDER BY created_at ASC LIMIT 1)
		  WHERE s.token_hash = $1`, hashToken(token)).
		Scan(&s.CSRFToken, &expires, &s.User.ID, &s.User.Email, &s.User.Role)
	if err == pgx.ErrNoRows {
		return Session{}, ErrUnauthorized
	}
	if err != nil {
		return Session{}, err
	}
	if nowUTC().After(expires) {
		_ = DeleteSession(ctx, pool, token)
		return Session{}, ErrUnauthorized
	}
	s.ExpiresAt = expires
	return s, nil
}

// DeleteSession revokes a session (logout).
func DeleteSession(ctx context.Context, pool *pgxpool.Pool, token string) error {
	_, err := pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, hashToken(token))
	return err
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// nowUTC is a seam for tests; production uses the wall clock.
var nowUTC = func() time.Time { return time.Now().UTC() }
