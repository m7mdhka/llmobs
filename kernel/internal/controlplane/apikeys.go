package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
)

// ErrInvalidScope is returned when a requested key scope is not permitted.
var ErrInvalidScope = errors.New("invalid or empty scope set")

// ErrScopeExceedsMinter is returned when a requested key scope grants authority the minting
// user does not personally hold — no privilege amplification via key minting: a derived
// credential can never carry scopes its deriver lacks.
var ErrScopeExceedsMinter = errors.New("requested key scope exceeds the minter's own authority")

// APIKeyInfo is a key as listed to an admin — never includes the secret.
type APIKeyInfo struct {
	PublicKey string   `json:"public_key"`
	Scopes    []string `json:"scopes"`
	CreatedAt string   `json:"created_at"`
}

// allowedScopes are the scopes an issued machine key may hold.
var allowedScopes = map[string]bool{
	"ingest": true, "query": true, "query:payloads": true, "scores:write": true, "delete": true,
}

// CreateAPIKey issues a new machine key for a project with the given scopes and
// returns the plaintext secret ONCE (only its argon2id hash + selector are
// stored). This lets any project mint its own scoped machine keys, replacing the
// earlier state where only the single system bootstrap key existed.
//
// createdByUserID records the provenance of the minting session — who created
// this credential, for audit and revocation. It is stored with ON DELETE SET NULL, so
// deleting that user never deletes their still-valid keys (breaking a tenant's ingestion),
// only drops the provenance link. An empty string stores NULL (e.g. the bootstrap key).
//
// minterScopes is the minter's OWN canonical role scopes. Every requested key scope must be
// granted by them (perm.KeyScopeGrantedBy) — a key can never carry authority its minter
// lacks (a member cannot mint an ingest/delete key). This cap lives at THIS one seam so
// every mint path inherits it by construction rather than re-checking per caller — the same
// parallel-credential discipline the frontend token already follows. A nil minterScopes means
// "no cap" and is reserved for the system-owned bootstrap key (no session); it is never nil
// on a user-driven mint.
func CreateAPIKey(ctx context.Context, pool *pgxpool.Pool, projectID string, scopes []string, createdByUserID string, minterScopes []string) (secret, publicKey string, err error) {
	for _, sc := range scopes {
		if !allowedScopes[sc] {
			return "", "", ErrInvalidScope
		}
		if minterScopes != nil && !perm.KeyScopeGrantedBy(minterScopes, sc) {
			return "", "", ErrScopeExceedsMinter
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
	var creator any
	if createdByUserID != "" {
		creator = createdByUserID
	}
	if _, err = pool.Exec(ctx,
		`INSERT INTO api_keys (public_key, project_id, lookup_hash, hashed_secret, scopes, created_by_user_id) VALUES ($1,$2,$3,$4,$5,$6)`,
		pub, projectID, lookup, hashed, scopes, creator); err != nil {
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
