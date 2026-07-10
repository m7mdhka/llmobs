package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PluginKV is the per-plugin key/value store (H4). Every operation is scoped by
// (plugin_id, project_id) so a plugin's kv is isolated per tenant and per plugin.
type PluginKV struct {
	pool *pgxpool.Pool
}

func NewPluginKV(pool *pgxpool.Pool) *PluginKV { return &PluginKV{pool: pool} }

// Get returns the value for a key, or (nil, false) if absent.
func (k *PluginKV) Get(ctx context.Context, pluginID, projectID, key string) (json.RawMessage, bool, error) {
	var v []byte
	err := k.pool.QueryRow(ctx,
		`SELECT value FROM plugin_kv WHERE plugin_id=$1 AND project_id=$2 AND key=$3`,
		pluginID, projectID, key).Scan(&v)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

// Set upserts a key's value.
func (k *PluginKV) Set(ctx context.Context, pluginID, projectID, key string, value json.RawMessage) error {
	_, err := k.pool.Exec(ctx,
		`INSERT INTO plugin_kv (plugin_id, project_id, key, value, updated_at)
		 VALUES ($1,$2,$3,$4, now())
		 ON CONFLICT (plugin_id, project_id, key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`,
		pluginID, projectID, key, []byte(value))
	return err
}

// Delete removes a key (no error if absent).
func (k *PluginKV) Delete(ctx context.Context, pluginID, projectID, key string) error {
	_, err := k.pool.Exec(ctx,
		`DELETE FROM plugin_kv WHERE plugin_id=$1 AND project_id=$2 AND key=$3`,
		pluginID, projectID, key)
	return err
}

// List returns the keys (sorted) with the given prefix.
func (k *PluginKV) List(ctx context.Context, pluginID, projectID, prefix string) ([]string, error) {
	rows, err := k.pool.Query(ctx,
		`SELECT key FROM plugin_kv WHERE plugin_id=$1 AND project_id=$2 AND key LIKE $3 ORDER BY key`,
		pluginID, projectID, prefix+"%")
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
