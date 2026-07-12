package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
)

// User is a resolved account. Role is the user's server-resolved membership
// role in the ACTIVE org — set by ResolveSession from org_memberships (default org in the
// single-org profile). It is NOT the legacy flat users.role and NOT a per-
// project authority: the request auth seam replaces this ambient field with per-project
// resolution (RoleInOrg(user, OrgForProject(project))). Do not treat it as
// authoritative for a project in a non-default org.
type User struct {
	ID    string
	Email string
	Role  string
}

// BootstrapAdmin ensures the single admin user exists. On first boot it creates
// the admin from the configured email/password (argon2id-hashed) and reports
// created=true so the daemon can log it once, mirroring the API-key bootstrap.
// Idempotent: if any user already exists it is left untouched.
func BootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, email, password string) (created bool, err error) {
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}
	if email == "" || password == "" {
		// No admin and no credentials configured: skip (the shell will have no
		// login until an admin is provisioned). Not an error — lite may run
		// headless for ingestion-only smoke tests.
		return false, nil
	}
	hash, herr := hashArgon2id(password)
	if herr != nil {
		return false, fmt.Errorf("hash admin password: %w", herr)
	}
	id, ierr := randomID("usr")
	if ierr != nil {
		return false, ierr
	}
	// The first admin is the org OWNER. Bootstrap (org/project) runs before
	// this, so the default org exists. The user insert AND the owner grant are ATOMIC (one
	// transaction): a partial failure must never leave an admin with no membership — that
	// would resolve to zero authority (fail closed) AND never self-heal (a later boot
	// no-ops on "a user exists"), permanently locking out the operator.
	orgID, oerr := DefaultOrgID(ctx, pool)
	if oerr != nil {
		return false, fmt.Errorf("resolve default org for admin membership: %w", oerr)
	}
	tx, terr := pool.Begin(ctx)
	if terr != nil {
		return false, terr
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, role) VALUES ($1,$2,$3,'admin')`,
		id, email, hash); err != nil {
		return false, fmt.Errorf("insert admin: %w", err)
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO org_memberships (user_id, org_id, role) VALUES ($1,$2,$3)`,
		id, orgID, perm.RoleOwner); err != nil {
		return false, fmt.Errorf("grant admin owner membership: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit admin bootstrap: %w", err)
	}
	return true, nil
}

// ErrBadCredentials is returned when an email/password pair does not authenticate.
var ErrBadCredentials = errors.New("bad credentials")

// VerifyPassword resolves an email+password to a User, or ErrBadCredentials. The
// argon2id verify runs even on unknown emails to keep timing uniform.
func VerifyPassword(ctx context.Context, pool *pgxpool.Pool, email, password string) (User, error) {
	var u User
	var hash *string // NULL for an SSO-only account (no local password)
	var revoked bool
	var localPwDisabled bool
	// Resolve the authoritative membership role (default org), exactly as ResolveSession
	// does — so the login response and /auth/me agree and the legacy users.role is never
	// surfaced to a client. No membership → "" → no scopes (fail closed). The revoked flag
	// and the default org's local-password-disabled flag are fetched in the SAME
	// query so no branch incurs an extra round-trip — the timing is identical across "wrong
	// password", "revoked", and "SSO-only" (no account-state enumeration by timing).
	err := pool.QueryRow(ctx,
		`SELECT u.id, u.email, COALESCE(m.role, ''), u.password_hash,
		        EXISTS(SELECT 1 FROM revocations r
		                WHERE r.principal_kind = 'user' AND r.principal_id = lower(u.email)),
		        COALESCE((SELECT s.local_password_disabled FROM sso_providers s
		                   WHERE s.org_id = (SELECT org_id FROM projects ORDER BY created_at ASC LIMIT 1)
		                     AND s.enabled = true), false)
		   FROM users u
		   LEFT JOIN org_memberships m
		     ON m.user_id = u.id
		    AND m.org_id = (SELECT org_id FROM projects ORDER BY created_at ASC LIMIT 1)
		  WHERE lower(u.email) = lower($1)`,
		strings.TrimSpace(email)).Scan(&u.ID, &u.Email, &u.Role, &hash, &revoked, &localPwDisabled)
	if err == pgx.ErrNoRows {
		// Verify against a dummy hash so a missing user and a wrong password take
		// the same time (mitigates user-enumeration by timing).
		_, _ = verifyArgon2id(password, dummyHash)
		return User{}, ErrBadCredentials
	}
	if err != nil {
		return User{}, err
	}
	// An SSO-only account (NULL hash) can NEVER log in locally. Still run the dummy verify so
	// the timing matches a normal wrong-password attempt.
	if hash == nil {
		_, _ = verifyArgon2id(password, dummyHash)
		return User{}, ErrBadCredentials
	}
	ok, verr := verifyArgon2id(password, *hash)
	if verr != nil || !ok {
		return User{}, ErrBadCredentials
	}
	// A revoked user must not be able to mint a fresh session and restart the derivation tree.
	// Denied as ErrBadCredentials so the response never reveals the account exists-but-revoked.
	if revoked {
		return User{}, ErrBadCredentials
	}
	// If the org has disabled local passwords, only OWNERS retain local login — the
	// break-glass path that makes last-owner lockout impossible. Everyone else must use SSO.
	if localPwDisabled && u.Role != perm.RoleOwner {
		return User{}, ErrBadCredentials
	}
	return u, nil
}

// dummyHash is a fixed argon2id hash of a random value, used to equalize timing
// on unknown-email logins. It never matches a real password.
var dummyHash, _ = hashArgon2id("llmobs-timing-equalizer-not-a-password")

// DefaultProjectID returns the single project's id (single-project lite). When
// project management arrives this is replaced by membership resolution.
func DefaultProjectID(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT id FROM projects ORDER BY created_at ASC LIMIT 1`).Scan(&id)
	return id, err
}

func randomID(prefix string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(buf), nil
}
