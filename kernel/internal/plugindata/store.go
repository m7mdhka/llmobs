package plugindata

import (
	"context"
	"encoding/json"
)

// Store is the PluginStore boundary: a plugin's own structured collections,
// kernel-owned and tenant-scoped. The lite adapter is Postgres (plugin-namespaced
// schemas in the shared DB); a dedicated-database backend slots in for scale
// behind this same interface — mirroring the telemetry storage-adapter seam. The
// plugin never touches the database; it reaches this only through the SDK `store`
// primitive over the double-token path.
type Store interface {
	// EnsureCollection idempotently provisions a plugin's collection (schema +
	// table + indexes + registry). Safe to re-run — this is what makes migration
	// recoverable across a kernel restart.
	EnsureCollection(ctx context.Context, pluginID string, c CollectionSpec) error
	// Put upserts a record by id, scoped to (plugin, project, collection).
	Put(ctx context.Context, pluginID, projectID, collection, id string, record json.RawMessage) error
	// Get returns a record by id, or (nil, false).
	Get(ctx context.Context, pluginID, projectID, collection, id string) (json.RawMessage, bool, error)
	// Query runs the read surface (filter/order/paginate on indexed fields),
	// returning the page and a next-page cursor ("" when exhausted).
	Query(ctx context.Context, pluginID, projectID, collection string, q Query) (rows []json.RawMessage, next string, err error)
	// Delete removes a record by id.
	Delete(ctx context.Context, pluginID, projectID, collection, id string) error
}
