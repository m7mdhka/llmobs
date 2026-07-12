package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PluginSecrets stores per-plugin encrypted secrets. It only ever holds
// ciphertext + nonce; encryption/decryption is the handler's job (secretbox).
type PluginSecrets struct {
	pool *pgxpool.Pool
}

func NewPluginSecrets(pool *pgxpool.Pool) *PluginSecrets { return &PluginSecrets{pool: pool} }

// SetEncrypted upserts a secret's ciphertext + nonce.
func (s *PluginSecrets) SetEncrypted(ctx context.Context, pluginID, projectID, name string, ciphertext, nonce []byte) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO plugin_secrets (plugin_id, project_id, name, ciphertext, nonce, updated_at)
		 VALUES ($1,$2,$3,$4,$5, now())
		 ON CONFLICT (plugin_id, project_id, name) DO UPDATE SET ciphertext=EXCLUDED.ciphertext, nonce=EXCLUDED.nonce, updated_at=now()`,
		pluginID, projectID, name, ciphertext, nonce)
	return err
}

// GetEncrypted returns a secret's ciphertext + nonce, or found=false.
func (s *PluginSecrets) GetEncrypted(ctx context.Context, pluginID, projectID, name string) (ciphertext, nonce []byte, found bool, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT ciphertext, nonce FROM plugin_secrets WHERE plugin_id=$1 AND project_id=$2 AND name=$3`,
		pluginID, projectID, name).Scan(&ciphertext, &nonce)
	if err == pgx.ErrNoRows {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	return ciphertext, nonce, true, nil
}

// ListNames returns the secret names set for a plugin+project (never values).
func (s *PluginSecrets) ListNames(ctx context.Context, pluginID, projectID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name FROM plugin_secrets WHERE plugin_id=$1 AND project_id=$2 ORDER BY name`,
		pluginID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Delete removes a secret (no error if absent).
func (s *PluginSecrets) Delete(ctx context.Context, pluginID, projectID, name string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM plugin_secrets WHERE plugin_id=$1 AND project_id=$2 AND name=$3`,
		pluginID, projectID, name)
	return err
}
