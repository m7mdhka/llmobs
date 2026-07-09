package postgres

import (
	"context"
	"encoding/json"
	"time"

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

	var existingDoc []byte
	var existingTS time.Time
	err = tx.QueryRow(ctx,
		`SELECT doc, event_ts FROM spans WHERE project_id=$1 AND id=$2 FOR UPDATE`,
		projectID, id).Scan(&existingDoc, &existingTS)

	var merged map[string]any
	switch err {
	case nil:
		var state map[string]any
		if uerr := json.Unmarshal(existingDoc, &state); uerr != nil {
			return uerr
		}
		merged = mergeInto(state, existingTS, ev)
	case pgx.ErrNoRows:
		merged = Fold("span", []Event{ev})
	default:
		return err
	}

	doc, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	c := extractSpanColumns(merged, ev.EventTS)
	_, err = tx.Exec(ctx, `
		INSERT INTO spans (project_id, id, trace_id, parent_span_id, kind, raw_kind, name,
			start_time, end_time, status_code, environment, release, version, session_id, user_id,
			model, provider, total_cost, attributes, usage_details, cost_details,
			provided_usage_details, provided_cost_details, prompt_ref, pricing_snapshot_ref,
			is_deleted, event_ts, doc, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,
			$19,$20,$21,$22,$23,$24,$25,$26,$27,$28, now())
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
			event_ts=EXCLUDED.event_ts, doc=EXCLUDED.doc, updated_at=now()`,
		projectID, id, c.traceID, c.parentSpanID, c.kind, c.rawKind, c.name,
		c.startTime, c.endTime, c.statusCode, c.environment, c.release, c.version, c.sessionID, c.userID,
		c.model, c.provider, c.totalCost, c.attributes, c.usageDetails, c.costDetails,
		c.providedUsageDetails, c.providedCostDetails, c.promptRef, c.pricingSnapshotRef,
		c.isDeleted, ev.EventTS.UTC(), doc)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

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

// mergeInto folds the current state (as a prior event at its event_ts) with a new
// event — the read-modify-write merge. The stored state's control fields
// (is_deleted, dq) are not re-folded as payload; dq history is preserved.
func mergeInto(state map[string]any, stateTS time.Time, ev Event) map[string]any {
	priorDQ, _ := state["dq"].(map[string]any)
	payload := map[string]any{}
	for k, v := range state {
		if k == "is_deleted" || k == "dq" {
			continue
		}
		payload[k] = v
	}
	prior := Event{Op: OpUpsert, EventTS: stateTS, EventID: "", Payload: payload}
	merged := Fold("span", []Event{prior, ev})
	if len(priorDQ) > 0 {
		newDQ, _ := merged["dq"].(map[string]any)
		if newDQ == nil {
			newDQ = map[string]any{}
		}
		for k, v := range priorDQ {
			if _, ok := newDQ[k]; !ok {
				newDQ[k] = v
			}
		}
		merged["dq"] = newDQ
	}
	return merged
}
