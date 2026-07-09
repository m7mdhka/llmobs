package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the lite-profile storage adapter.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// PersistSpan applies one span event with **merge-on-write under the row lock**
// (LM-5 observable semantics): read the current row FOR UPDATE, fold the current
// state together with the incoming event, and write the result. On first sight of
// an id, the folded single-event state is inserted.
func (s *Store) PersistSpan(ctx context.Context, ev Event) error {
	projectID, _ := ev.Payload["project_id"].(string)
	id, _ := ev.Payload["id"].(string)
	if projectID == "" || id == "" {
		return errMissingIdentity
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var existingDoc, existingProv []byte
	err = tx.QueryRow(ctx,
		`SELECT doc, provenance FROM spans WHERE project_id=$1 AND id=$2 FOR UPDATE`,
		projectID, id).Scan(&existingDoc, &existingProv)

	state := map[string]any{}
	prov := Provenance{}
	switch err {
	case nil:
		if uerr := json.Unmarshal(existingDoc, &state); uerr != nil {
			return uerr
		}
		if len(existingProv) > 0 {
			if uerr := json.Unmarshal(existingProv, &prov); uerr != nil {
				return uerr
			}
		}
	case pgx.ErrNoRows:
		// first sight of this id: fold against empty state+provenance
	default:
		return err
	}

	// Per-field provenance fold under the row lock (issue #17): incoming event
	// folds against per-group stamps, so out-of-order updates converge to the
	// same state as the ordered Fold.
	merged, newProv := MergeEvent("span", state, prov, ev)

	doc, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	provJSON, err := json.Marshal(newProv)
	if err != nil {
		return err
	}
	c := extractSpanColumns(merged, ev.EventTS)
	_, err = tx.Exec(ctx, `
		INSERT INTO spans (project_id, id, trace_id, parent_span_id, kind, raw_kind, name,
			start_time, end_time, status_code, environment, release, version, session_id, user_id,
			model, provider, total_cost, attributes, usage_details, cost_details,
			provided_usage_details, provided_cost_details, prompt_ref, pricing_snapshot_ref,
			is_deleted, event_ts, doc, provenance, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,
			$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29, now())
		ON CONFLICT (project_id, id) DO UPDATE SET
			trace_id=EXCLUDED.trace_id, parent_span_id=EXCLUDED.parent_span_id, kind=EXCLUDED.kind,
			raw_kind=EXCLUDED.raw_kind, name=EXCLUDED.name, start_time=EXCLUDED.start_time,
			end_time=EXCLUDED.end_time, status_code=EXCLUDED.status_code, environment=EXCLUDED.environment,
			release=EXCLUDED.release, version=EXCLUDED.version, session_id=EXCLUDED.session_id,
			user_id=EXCLUDED.user_id, model=EXCLUDED.model, provider=EXCLUDED.provider,
			total_cost=EXCLUDED.total_cost, attributes=EXCLUDED.attributes, usage_details=EXCLUDED.usage_details,
			cost_details=EXCLUDED.cost_details, provided_usage_details=EXCLUDED.provided_usage_details,
			provided_cost_details=EXCLUDED.provided_cost_details, prompt_ref=EXCLUDED.prompt_ref,
			pricing_snapshot_ref=EXCLUDED.pricing_snapshot_ref, is_deleted=EXCLUDED.is_deleted,
			event_ts=EXCLUDED.event_ts, doc=EXCLUDED.doc, provenance=EXCLUDED.provenance, updated_at=now()`,
		projectID, id, c.traceID, c.parentSpanID, c.kind, c.rawKind, c.name,
		c.startTime, c.endTime, c.statusCode, c.environment, c.release, c.version, c.sessionID, c.userID,
		c.model, c.provider, c.totalCost, c.attributes, c.usageDetails, c.costDetails,
		c.providedUsageDetails, c.providedCostDetails, c.promptRef, c.pricingSnapshotRef,
		c.isDeleted, ev.EventTS.UTC(), doc, provJSON)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// (merge-on-write folds via MergeEvent above; see merge.go for the fold.)

// GetSpan returns the folded span document by id, or nil if absent/deleted.
func (s *Store) GetSpan(ctx context.Context, projectID, id string) (json.RawMessage, error) {
	var doc []byte
	err := s.pool.QueryRow(ctx,
		`SELECT doc FROM spans WHERE project_id=$1 AND id=$2 AND is_deleted=false`,
		projectID, id).Scan(&doc)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return doc, err
}

// GetTraceSpans returns all non-deleted spans of a trace (folded docs). Ordered
// by (start_time, id) so the tree assembler produces a deterministic preorder.
func (s *Store) GetTraceSpans(ctx context.Context, projectID, traceID string) ([]json.RawMessage, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT doc FROM spans WHERE project_id=$1 AND trace_id=$2 AND is_deleted=false
		 ORDER BY start_time ASC, id ASC`, projectID, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var doc []byte
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, rows.Err()
}

// QuerySpans runs a compiled spans query and returns the folded documents.
// where is a SQL predicate (excluding project/is_deleted, added here); args are
// its parameters starting at $1; order and limit come from the compiler.
func (s *Store) QuerySpans(ctx context.Context, where string, args []any, order string, limit int) ([]json.RawMessage, error) {
	sql := `SELECT doc FROM spans WHERE is_deleted=false`
	if where != "" {
		sql += " AND (" + where + ")"
	}
	if order != "" {
		sql += " ORDER BY " + order
	}
	sql += " LIMIT " + itoa(limit)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var doc []byte
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, rows.Err()
}
