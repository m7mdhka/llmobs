package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/plugindata"
)

// PluginStore is the lite-profile PluginStore adapter (R1): plugin-namespaced
// Postgres schemas in the shared database. It provisions a schema + table +
// indexes per collection, and serves tenant-scoped CRUD + the R4 query surface.
type PluginStore struct {
	pool *pgxpool.Pool

	mu    sync.RWMutex
	specs map[string]plugindata.CollectionSpec // key: pluginID\x00collection
}

func NewPluginStore(pool *pgxpool.Pool) *PluginStore {
	return &PluginStore{pool: pool, specs: map[string]plugindata.CollectionSpec{}}
}

func specKey(pluginID, collection string) string { return pluginID + "\x00" + collection }

// Provision ensures all of a plugin's collections exist (satisfies
// supervisor.Provisioner). Idempotent — safe to re-run on every re-handshake.
func (s *PluginStore) Provision(ctx context.Context, pluginID string, collections []plugindata.CollectionSpec) error {
	for _, c := range collections {
		if err := s.EnsureCollection(ctx, pluginID, c); err != nil {
			return err
		}
	}
	return nil
}

func sqlType(t plugindata.FieldType) string {
	switch t {
	case plugindata.FieldNumber:
		return "DOUBLE PRECISION"
	case plugindata.FieldBool:
		return "BOOLEAN"
	default:
		return "TEXT"
	}
}

// EnsureCollection idempotently provisions a collection: schema, table (with the
// indexed fields promoted to typed columns), per-index, and the registry row. Safe
// to re-run — the DDL is all IF NOT EXISTS, so a kernel restart mid-provision
// recovers on the next call.
func (s *PluginStore) EnsureCollection(ctx context.Context, pluginID string, c plugindata.CollectionSpec) error {
	if err := c.Validate(); err != nil {
		return err
	}
	schema, err := plugindata.SchemaName(pluginID)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %q`, schema)); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	var cols strings.Builder
	for _, f := range c.Fields {
		if f.Indexed {
			fmt.Fprintf(&cols, `, %q %s`, f.Name, sqlType(f.Type))
		}
	}
	ddl := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %q.%q (
		id TEXT NOT NULL, project_id TEXT NOT NULL%s,
		doc JSONB NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (project_id, id))`, schema, c.Name, cols.String())
	if _, err := tx.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	for name := range c.Indexed() {
		idx := fmt.Sprintf(`idx_%s_%s`, c.Name, name)
		if _, err := tx.Exec(ctx, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %q ON %q.%q (project_id, %q)`, idx, schema, c.Name, name)); err != nil {
			return fmt.Errorf("create index: %w", err)
		}
	}
	specJSON, _ := json.Marshal(c)
	if _, err := tx.Exec(ctx,
		`INSERT INTO plugin_collections (plugin_id, collection, spec, updated_at) VALUES ($1,$2,$3, now())
		 ON CONFLICT (plugin_id, collection) DO UPDATE SET spec=EXCLUDED.spec, updated_at=now()`,
		pluginID, c.Name, specJSON); err != nil {
		return fmt.Errorf("registry upsert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.specs[specKey(pluginID, c.Name)] = c
	s.mu.Unlock()
	return nil
}

// spec returns a collection's spec, loading it from the registry on a cache miss.
func (s *PluginStore) spec(ctx context.Context, pluginID, collection string) (plugindata.CollectionSpec, error) {
	s.mu.RLock()
	c, ok := s.specs[specKey(pluginID, collection)]
	s.mu.RUnlock()
	if ok {
		return c, nil
	}
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT spec FROM plugin_collections WHERE plugin_id=$1 AND collection=$2`, pluginID, collection).Scan(&raw)
	if err == pgx.ErrNoRows {
		return plugindata.CollectionSpec{}, fmt.Errorf("plugindata: unknown collection %q", collection)
	}
	if err != nil {
		return plugindata.CollectionSpec{}, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return plugindata.CollectionSpec{}, err
	}
	s.mu.Lock()
	s.specs[specKey(pluginID, collection)] = c
	s.mu.Unlock()
	return c, nil
}

func (s *PluginStore) Put(ctx context.Context, pluginID, projectID, collection, id string, record json.RawMessage) error {
	c, err := s.spec(ctx, pluginID, collection)
	if err != nil {
		return err
	}
	schema, err := plugindata.SchemaName(pluginID)
	if err != nil {
		return err
	}
	var rec map[string]any
	if err := json.Unmarshal(record, &rec); err != nil {
		return fmt.Errorf("plugindata: record must be a JSON object")
	}
	// Columns: id, project_id, <indexed fields...>, doc, updated_at.
	cols := []string{`"id"`, `"project_id"`}
	vals := []any{id, projectID}
	var set []string
	for name := range c.Indexed() {
		cols = append(cols, fmt.Sprintf(`%q`, name))
		vals = append(vals, rec[name]) // nil when absent
		set = append(set, fmt.Sprintf(`%q=EXCLUDED.%q`, name, name))
	}
	cols = append(cols, `"doc"`, `"updated_at"`)
	vals = append(vals, []byte(record))
	set = append(set, `"doc"=EXCLUDED."doc"`, `"updated_at"=now()`)

	ph := make([]string, len(vals))
	for i := range vals {
		ph[i] = fmt.Sprintf("$%d", i+1)
	}
	sql := fmt.Sprintf(`INSERT INTO %q.%q (%s) VALUES (%s, now()) ON CONFLICT (project_id, id) DO UPDATE SET %s`,
		schema, collection, strings.Join(cols, ", "), strings.Join(ph, ", "), strings.Join(set, ", "))
	_, err = s.pool.Exec(ctx, sql, vals...)
	return err
}

func (s *PluginStore) Get(ctx context.Context, pluginID, projectID, collection, id string) (json.RawMessage, bool, error) {
	if _, err := s.spec(ctx, pluginID, collection); err != nil {
		return nil, false, err
	}
	schema, err := plugindata.SchemaName(pluginID)
	if err != nil {
		return nil, false, err
	}
	var doc []byte
	err = s.pool.QueryRow(ctx, fmt.Sprintf(`SELECT doc FROM %q.%q WHERE project_id=$1 AND id=$2`, schema, collection), projectID, id).Scan(&doc)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return doc, true, nil
}

func (s *PluginStore) Query(ctx context.Context, pluginID, projectID, collection string, q plugindata.Query) ([]json.RawMessage, string, error) {
	c, err := s.spec(ctx, pluginID, collection)
	if err != nil {
		return nil, "", err
	}
	schema, err := plugindata.SchemaName(pluginID)
	if err != nil {
		return nil, "", err
	}
	sql, args, limit, err := plugindata.CompileSelect(schema, collection, c.Indexed(), projectID, q)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	type row struct {
		id  string
		doc json.RawMessage
	}
	var all []row
	for rows.Next() {
		var r row
		var doc []byte
		if err := rows.Scan(&r.id, &doc); err != nil {
			return nil, "", err
		}
		r.doc = doc
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(all) > limit {
		last := all[limit-1]
		var orderVal any
		if q.Order != nil {
			var m map[string]any
			_ = json.Unmarshal(last.doc, &m)
			orderVal = m[q.Order.Field]
		}
		next = plugindata.EncodeCursor(orderVal, last.id)
		all = all[:limit]
	}
	out := make([]json.RawMessage, len(all))
	for i, r := range all {
		out[i] = r.doc
	}
	return out, next, nil
}

func (s *PluginStore) Delete(ctx context.Context, pluginID, projectID, collection, id string) error {
	if _, err := s.spec(ctx, pluginID, collection); err != nil {
		return err
	}
	schema, err := plugindata.SchemaName(pluginID)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, fmt.Sprintf(`DELETE FROM %q.%q WHERE project_id=$1 AND id=$2`, schema, collection), projectID, id)
	return err
}
