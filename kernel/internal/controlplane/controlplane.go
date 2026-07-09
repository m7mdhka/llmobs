// Package controlplane is the minimal control plane ingestion + query need:
// organizations, projects, and API keys, plus a lite-profile bootstrap. Users,
// sessions, OIDC, and RBAC are deliberately later; this package is structured so
// they slot in. (B1 hashes keys with SHA-256; B2 upgrades to argon2id.)
package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrUnauthorized is returned when a bearer secret does not resolve to a key.
var ErrUnauthorized = errors.New("unauthorized")

// Identity is the resolved caller for an ingestion or query request.
type Identity struct {
	ProjectID string
	Scopes    []string
}

// HasScope reports whether the identity holds a scope (e.g. "ingest", "query").
func (id Identity) HasScope(s string) bool {
	for _, x := range id.Scopes {
		if x == s {
			return true
		}
	}
	return false
}

// hashSecret is the B1 key hash (SHA-256 hex). B2 replaces this with argon2id.
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Authenticate resolves a bearer secret to an Identity.
func Authenticate(ctx context.Context, pool *pgxpool.Pool, bearer string) (Identity, error) {
	if bearer == "" {
		return Identity{}, ErrUnauthorized
	}
	var id Identity
	err := pool.QueryRow(ctx,
		`SELECT project_id, scopes FROM api_keys WHERE hashed_secret=$1`,
		hashSecret(bearer)).Scan(&id.ProjectID, &id.Scopes)
	if err == pgx.ErrNoRows {
		return Identity{}, ErrUnauthorized
	}
	if err != nil {
		return Identity{}, err
	}
	return id, nil
}

// Bootstrap ensures a default org/project and an api key exist for the lite
// profile. If apiKey is empty a random one is generated. Returns the plaintext
// key so the daemon can print it once. Idempotent: existing keys are left as-is.
func Bootstrap(ctx context.Context, pool *pgxpool.Pool, projectName, apiKey string) (project, key string, created bool, err error) {
	const orgID, projectID = "org_default", "proj_default"

	if _, err = pool.Exec(ctx,
		`INSERT INTO organizations (id, name) VALUES ($1,$2) ON CONFLICT (id) DO NOTHING`,
		orgID, "default"); err != nil {
		return
	}
	if _, err = pool.Exec(ctx,
		`INSERT INTO projects (id, org_id, name) VALUES ($1,$2,$3) ON CONFLICT (id) DO NOTHING`,
		projectID, orgID, projectName); err != nil {
		return
	}

	var existing int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE project_id=$1`, projectID).Scan(&existing); err != nil {
		return
	}
	if existing > 0 {
		return projectID, "", false, nil
	}

	if apiKey == "" {
		buf := make([]byte, 24)
		if _, err = rand.Read(buf); err != nil {
			return
		}
		apiKey = "sk-" + hex.EncodeToString(buf)
	}
	pub := "pk-" + hashSecret(apiKey)[:16]
	if _, err = pool.Exec(ctx,
		`INSERT INTO api_keys (public_key, project_id, hashed_secret, scopes) VALUES ($1,$2,$3,$4)`,
		pub, projectID, hashSecret(apiKey), []string{"ingest", "query"}); err != nil {
		return "", "", false, fmt.Errorf("insert api key: %w", err)
	}
	return projectID, apiKey, true, nil
}
