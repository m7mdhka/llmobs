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
)

// User is a resolved account. v1alpha1 has a single admin; role is carried now so
// RBAC slots in without a migration.
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
	if _, err = pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, role) VALUES ($1,$2,$3,'admin')`,
		id, email, hash); err != nil {
		return false, fmt.Errorf("insert admin: %w", err)
	}
	return true, nil
}

// ErrBadCredentials is returned when an email/password pair does not authenticate.
var ErrBadCredentials = errors.New("bad credentials")

// VerifyPassword resolves an email+password to a User, or ErrBadCredentials. The
// argon2id verify runs even on unknown emails to keep timing uniform.
func VerifyPassword(ctx context.Context, pool *pgxpool.Pool, email, password string) (User, error) {
	var u User
	var hash string
	err := pool.QueryRow(ctx,
		`SELECT id, email, role, password_hash FROM users WHERE lower(email)=lower($1)`,
		strings.TrimSpace(email)).Scan(&u.ID, &u.Email, &u.Role, &hash)
	if err == pgx.ErrNoRows {
		// Verify against a dummy hash so a missing user and a wrong password take
		// the same time (mitigates user-enumeration by timing).
		_, _ = verifyArgon2id(password, dummyHash)
		return User{}, ErrBadCredentials
	}
	if err != nil {
		return User{}, err
	}
	ok, verr := verifyArgon2id(password, hash)
	if verr != nil || !ok {
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
