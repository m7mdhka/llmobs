package clickhouse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/merge"
)

var (
	errMissingIdentity      = errors.New("span event missing project_id or id")
	errMissingScoreIdentity = errors.New("score event missing project_id or id")
)

// writeConn is the narrow ClickHouse surface the read+write paths need, declared
// at the consumer per go-style. clickhouse-go's driver.Conn satisfies it.
type writeConn interface {
	Query(ctx context.Context, query string, args ...any) (driver.Rows, error)
	Exec(ctx context.Context, query string, args ...any) error
	PrepareBatch(ctx context.Context, query string, opts ...driver.PrepareBatchOption) (driver.Batch, error)
}

// Store is the scale-profile (ClickHouse) storage adapter. L1 implements the
// write side: merge-on-write producing one settled row per (project_id, id) via
// the SHARED merge fold (kernel/internal/storage/merge) — the identical fold the
// lite adapter runs, so the two engines can only diverge on the SQL surface,
// which is exactly what SQL-level conformance (L2) tests. The read side (DSL
// compiler + resource limits) lands in L2; this type does not yet implement the
// full TelemetryStore.
type Store struct {
	conn           writeConn
	suppressionTTL time.Duration // erasure-tombstone retention (G3)
	lastVer        atomic.Int64  // monotonic write-order stamp (RMT version)
	limits         ReadLimits    // per-query resource caps (RULING-CH9); fail-closed
	// guardLazyMaterialization appends query_plan_optimize_lazy_materialization=0 to
	// every read when the server HAS that setting (#88). Lazy materialization can, on a
	// plan reorder, surface a lightweight-deleted (GDPR-erased) row past the deleted
	// mask; disabling it on reads closes that read-back. Feature-detected at boot so we
	// never send the setting to a ClickHouse version that lacks it.
	guardLazyMaterialization bool
}

// SetReadLimits configures the mandatory per-query resource caps (RULING-CH9).
// Until set to a fully-valid value, every DSL read fails closed — the adapter
// refuses to emit an unbounded ClickHouse read.
func (s *Store) SetReadLimits(l ReadLimits) { s.limits = l }

// SetLazyMaterializationGuard enables the read-side lazy-materialization guard (#88)
// when the server supports the setting (feature-detected at boot). See the Store field.
func (s *Store) SetLazyMaterializationGuard(on bool) { s.guardLazyMaterialization = on }

// nextVer returns a strictly-increasing write-order stamp for the ReplacingMergeTree
// version column. It tracks wall-clock nanoseconds (so it is comparable across nodes
// and process restarts, given NTP + per-key write serialization) but never repeats or
// regresses within a process — two writes in the same clock tick still get distinct,
// increasing versions, so the later fold always wins the RMT collapse.
func (s *Store) nextVer() uint64 {
	for {
		prev := s.lastVer.Load()
		next := time.Now().UnixNano()
		if next <= prev {
			next = prev + 1
		}
		if s.lastVer.CompareAndSwap(prev, next) {
			return uint64(next)
		}
	}
}

// defaultSuppressionTTL mirrors the lite adapter: retain erasure tombstones long
// enough to outlast plausible redelivery, then they may be reaped.
const defaultSuppressionTTL = 720 * time.Hour // 30 days

// Store implements the full telemetry storage contract (read + write).
var _ storage.TelemetryStore = (*Store)(nil)

// NewStore builds the ClickHouse adapter over an established connection.
func NewStore(conn writeConn) *Store {
	return &Store{conn: conn, suppressionTTL: defaultSuppressionTTL}
}

// SetErasureSuppressionTTL overrides how long erasure tombstones block
// re-delivery of an erased span (G3). Non-positive values are ignored.
func (s *Store) SetErasureSuppressionTTL(d time.Duration) {
	if d > 0 {
		s.suppressionTTL = d
	}
}

// PersistSpan folds the incoming event into the current settled row for
// (project_id, id) and inserts the merged result (insert-only; ReplacingMergeTree
// collapses to the latest by event_ts, and reads pick the latest — never an
// in-place mutation, which CH does not do synchronously). The fold is the shared
// canonical fold, so out-of-order events converge exactly as in lite.
//
// G3: if an unexpired erasure tombstone covers this key, the write is suppressed
// and ErrSuppressedByErasure is returned (not a storage failure) so the caller
// drops the span without marking persistence unhealthy — the same contract as lite.
func (s *Store) PersistSpan(ctx context.Context, ev storage.Event) error {
	projectID, _ := ev.Payload["project_id"].(string)
	id, _ := ev.Payload["id"].(string)
	if projectID == "" || id == "" {
		return errMissingIdentity
	}

	suppressed, err := s.suppressed(ctx, projectID, id)
	if err != nil {
		return err
	}
	if suppressed {
		return storage.ErrSuppressedByErasure
	}

	state, prov, err := s.readSettled(ctx, "spans", projectID, id)
	if err != nil {
		return err
	}
	merged, newProv := merge.MergeEvent("span", state, prov, ev)
	doc, provJSON, err := marshalDocProv(merged, newProv)
	if err != nil {
		return err
	}

	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO spans (
		project_id, id, trace_id, parent_span_id, kind, raw_kind, name,
		start_time, end_time, completion_start_time, status_code, environment, release, version,
		session_id, user_id, model, provider, total_cost, attributes, usage_details, cost_details,
		provided_usage_details, provided_cost_details, prompt_ref, pricing_snapshot_ref,
		is_deleted, event_ts, ver, doc, provenance)`)
	if err != nil {
		return fmt.Errorf("prepare span insert: %w", err)
	}
	if err := batch.Append(
		projectID, id,
		str(merged, "trace_id"), str(merged, "parent_span_id"), str(merged, "kind"),
		str(merged, "raw_kind"), str(merged, "name"),
		timeReq(merged, "start_time", ev.EventTS), timeOpt(merged, "end_time"), timeOpt(merged, "completion_start_time"),
		statusStr(merged), str(merged, "environment"), str(merged, "release"), str(merged, "version"),
		str(merged, "session_id"), str(merged, "user_id"), str(merged, "model"), str(merged, "provider"),
		floatOpt(merged, "total_cost"),
		jsonMap(merged, "attributes"), jsonMap(merged, "usage_details"), jsonMap(merged, "cost_details"),
		jsonMap(merged, "provided_usage_details"), jsonMap(merged, "provided_cost_details"),
		jsonRef(merged, "prompt_ref"), jsonRef(merged, "pricing_snapshot_ref"),
		boolU8(merged, "is_deleted"), ev.EventTS.UTC(), s.nextVer(), doc, provJSON,
	); err != nil {
		return fmt.Errorf("append span row: %w", err)
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send span insert: %w", err)
	}
	return nil
}

// PersistScore folds and inserts a score's settled row, mirroring PersistSpan.
func (s *Store) PersistScore(ctx context.Context, ev storage.Event) error {
	projectID, _ := ev.Payload["project_id"].(string)
	id, _ := ev.Payload["id"].(string)
	if projectID == "" || id == "" {
		return errMissingScoreIdentity
	}

	state, prov, err := s.readSettled(ctx, "scores", projectID, id)
	if err != nil {
		return err
	}
	merged, newProv := merge.MergeEvent("score", state, prov, ev)
	doc, provJSON, err := marshalDocProv(merged, newProv)
	if err != nil {
		return err
	}

	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO scores (
		project_id, id, subject_type, subject_id, name, data_type,
		value_numeric, value_string, source, timestamp, environment, comment,
		metadata, config_ref, is_deleted, event_ts, ver, doc, provenance)`)
	if err != nil {
		return fmt.Errorf("prepare score insert: %w", err)
	}
	if err := batch.Append(
		projectID, id,
		str(merged, "subject_type"), str(merged, "subject_id"), str(merged, "name"), str(merged, "data_type"),
		floatOpt(merged, "value_numeric"), str(merged, "value_string"), str(merged, "source"),
		timeReq(merged, "timestamp", ev.EventTS), str(merged, "environment"), str(merged, "comment"),
		jsonMap(merged, "metadata"), jsonRef(merged, "config_ref"),
		boolU8(merged, "is_deleted"), ev.EventTS.UTC(), s.nextVer(), doc, provJSON,
	); err != nil {
		return fmt.Errorf("append score row: %w", err)
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send score insert: %w", err)
	}
	return nil
}

// readSettled fetches the current settled doc+provenance for (project_id, id),
// deduplicating to the latest by event_ts (LIMIT 1 BY on the sort-key prefix).
// Absent row → empty state+provenance (first sight of this id).
func (s *Store) readSettled(ctx context.Context, table, projectID, id string) (map[string]any, merge.Provenance, error) {
	// Dedup to the latest settled row by write-order `ver` (see the schema comment:
	// the last write folds in every prior event, so highest ver = most complete).
	q := fmt.Sprintf(
		`SELECT doc, provenance FROM %s WHERE project_id = ? AND id = ? ORDER BY ver DESC LIMIT 1`,
		table)
	rows, err := s.conn.Query(ctx, q, projectID, id)
	if err != nil {
		return nil, nil, fmt.Errorf("read settled %s: %w", table, err)
	}
	defer rows.Close()

	state := map[string]any{}
	prov := merge.Provenance{}
	if rows.Next() {
		var docStr, provStr string
		if err := rows.Scan(&docStr, &provStr); err != nil {
			return nil, nil, fmt.Errorf("scan settled %s: %w", table, err)
		}
		if docStr != "" {
			if err := json.Unmarshal([]byte(docStr), &state); err != nil {
				return nil, nil, fmt.Errorf("unmarshal settled doc: %w", err)
			}
		}
		if provStr != "" {
			if err := json.Unmarshal([]byte(provStr), &prov); err != nil {
				return nil, nil, fmt.Errorf("unmarshal settled provenance: %w", err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate settled %s: %w", table, err)
	}
	return state, prov, nil
}

// suppressed reports whether an unexpired erasure tombstone covers (project_id, id).
// Expiry is evaluated against ClickHouse's now64() — SERVER time — never the
// incoming event's timestamp: ev.EventTS is attacker-controlled OTLP data, and
// comparing against it would let a re-delivered erased span carry a future
// timestamp to skip suppression and resurrect the erased row inside its retention
// window (a GDPR-erasure bypass). This mirrors the lite adapter's `expires_at >
// now()` guard exactly. Conservative by design: any unexpired tombstone suppresses.
func (s *Store) suppressed(ctx context.Context, projectID, id string) (bool, error) {
	rows, err := s.conn.Query(ctx,
		`SELECT count() FROM erasure_suppression WHERE project_id = ? AND id = ? AND expires_at > now64(6)`,
		projectID, id)
	if err != nil {
		return false, fmt.Errorf("read erasure_suppression: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var n uint64
		if err := rows.Scan(&n); err != nil {
			return false, fmt.Errorf("scan erasure_suppression: %w", err)
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, rows.Err()
}

func marshalDocProv(doc map[string]any, prov merge.Provenance) (string, string, error) {
	d, err := json.Marshal(doc)
	if err != nil {
		return "", "", fmt.Errorf("marshal doc: %w", err)
	}
	p, err := json.Marshal(prov)
	if err != nil {
		return "", "", fmt.Errorf("marshal provenance: %w", err)
	}
	return string(d), string(p), nil
}
