package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PluginKV is the per-plugin key/value store. Every operation is scoped by
// (plugin_id, project_id, user_id) so a plugin's kv is isolated per tenant and per plugin,
// and optionally per USER. userID == "" is PROJECT scope (shared across the
// project's users); a non-empty userID (the kernel-resolved acting user) is USER scope, isolated
// so one user's per-user state is invisible to another in the same project.
type PluginKV struct {
	pool *pgxpool.Pool
}

func NewPluginKV(pool *pgxpool.Pool) *PluginKV { return &PluginKV{pool: pool} }

// Get returns the value for a key, or (nil, false) if absent.
func (k *PluginKV) Get(ctx context.Context, pluginID, projectID, userID, key string) (json.RawMessage, bool, error) {
	var v []byte
	err := k.pool.QueryRow(ctx,
		`SELECT value FROM plugin_kv WHERE plugin_id=$1 AND project_id=$2 AND user_id=$3 AND key=$4`,
		pluginID, projectID, userID, key).Scan(&v)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

// Set upserts a key's value.
func (k *PluginKV) Set(ctx context.Context, pluginID, projectID, userID, key string, value json.RawMessage) error {
	_, err := k.pool.Exec(ctx,
		`INSERT INTO plugin_kv (plugin_id, project_id, user_id, key, value, updated_at)
		 VALUES ($1,$2,$3,$4,$5, now())
		 ON CONFLICT (plugin_id, project_id, user_id, key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`,
		pluginID, projectID, userID, key, []byte(value))
	return err
}

// Delete removes a key (no error if absent).
func (k *PluginKV) Delete(ctx context.Context, pluginID, projectID, userID, key string) error {
	_, err := k.pool.Exec(ctx,
		`DELETE FROM plugin_kv WHERE plugin_id=$1 AND project_id=$2 AND user_id=$3 AND key=$4`,
		pluginID, projectID, userID, key)
	return err
}

// List returns the keys (sorted) with the given prefix.
func (k *PluginKV) List(ctx context.Context, pluginID, projectID, userID, prefix string) ([]string, error) {
	rows, err := k.pool.Query(ctx,
		`SELECT key FROM plugin_kv WHERE plugin_id=$1 AND project_id=$2 AND user_id=$3 AND key LIKE $4 ORDER BY key`,
		pluginID, projectID, userID, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}
