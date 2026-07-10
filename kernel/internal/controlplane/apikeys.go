package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInvalidScope is returned when a requested key scope is not permitted.
var ErrInvalidScope = errors.New("invalid or empty scope set")

// APIKeyInfo is a key as listed to an admin — never includes the secret.
type APIKeyInfo struct {
	PublicKey string   `json:"public_key"`
	Scopes    []string `json:"scopes"`
	CreatedAt string   `json:"created_at"`
}

// allowedScopes are the scopes an issued machine key may hold.
var allowedScopes = map[string]bool{
	"ingest": true, "query": true, "scores:write": true, "delete": true,
}

// CreateAPIKey issues a new machine key for a project with the given scopes and
// returns the plaintext secret ONCE (only its argon2id hash + selector are
// stored). This is the fix for the "bootstrap key only" gap the audit found
// across Stories 9, 10, 19.
func CreateAPIKey(ctx context.Context, pool *pgxpool.Pool, projectID string, scopes []string) (secret, publicKey string, err error) {
	for _, sc := range scopes {
		if !allowedScopes[sc] {
			return "", "", ErrInvalidScope
		}
	}
	if len(scopes) == 0 {
		return "", "", ErrInvalidScope
	}
	buf := make([]byte, 24)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	secret = "sk-" + hex.EncodeToString(buf)
	lookup := selector(secret)
	hashed, herr := hashArgon2id(secret)
	if herr != nil {
		return "", "", herr
	}
	pub := "pk-" + lookup[:16]
	if _, err = pool.Exec(ctx,
		`INSERT INTO api_keys (public_key, project_id, lookup_hash, hashed_secret, scopes) VALUES ($1,$2,$3,$4,$5)`,
		pub, projectID, lookup, hashed, scopes); err != nil {
		return "", "", err
	}
	return secret, pub, nil
}

// ListAPIKeys returns the project's keys (public prefix + scopes only).
func ListAPIKeys(ctx context.Context, pool *pgxpool.Pool, projectID string) ([]APIKeyInfo, error) {
	rows, err := pool.Query(ctx,
		`SELECT public_key, scopes, created_at FROM api_keys WHERE project_id=$1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKeyInfo
	for rows.Next() {
		var k APIKeyInfo
		var created time.Time
		if err := rows.Scan(&k.PublicKey, &k.Scopes, &created); err != nil {
			return nil, err
		}
		k.CreatedAt = created.UTC().Format(time.RFC3339)
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeAPIKey deletes a key by its public id within the project.
func RevokeAPIKey(ctx context.Context, pool *pgxpool.Pool, projectID, publicKey string) error {
	_, err := pool.Exec(ctx, `DELETE FROM api_keys WHERE project_id=$1 AND public_key=$2`, projectID, publicKey)
	return err
}
