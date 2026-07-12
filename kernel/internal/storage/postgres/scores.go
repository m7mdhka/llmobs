package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/merge"
)

var errMissingScoreIdentity = errors.New("score event missing project_id or id")

// PersistScore applies one score event with merge-on-write (LM-3/LM-8), the same
// per-field provenance fold as spans (entity type "score"). Idempotency key is
// (project_id, id): reusing an id overwrites (05-update-semantics.md, 04 §6).
func (s *Store) PersistScore(ctx context.Context, ev storage.Event) error {
	projectID, _ := ev.Payload["project_id"].(string)
	id, _ := ev.Payload["id"].(string)
	if projectID == "" || id == "" {
		return errMissingScoreIdentity
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var existingDoc, existingProv []byte
	err = tx.QueryRow(ctx,
		`SELECT doc, provenance FROM scores WHERE project_id=$1 AND id=$2 FOR UPDATE`,
		projectID, id).Scan(&existingDoc, &existingProv)
	state := map[string]any{}
	prov := merge.Provenance{}
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
	default:
		return err
	}

	merged, newProv := merge.MergeEvent("score", state, prov, ev)
	doc, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	provJSON, err := json.Marshal(newProv)
	if err != nil {
		return err
	}
	c := extractScoreColumns(merged)
	_, err = tx.Exec(ctx, `
		INSERT INTO scores (project_id, id, subject_type, subject_id, name, data_type,
			value_numeric, value_string, source, timestamp, environment, comment,
			metadata, config_ref, is_deleted, event_ts, doc, provenance, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18, now())
		ON CONFLICT (project_id, id) DO UPDATE SET
			subject_type=EXCLUDED.subject_type, subject_id=EXCLUDED.subject_id, name=EXCLUDED.name,
			data_type=EXCLUDED.data_type, value_numeric=EXCLUDED.value_numeric,
			value_string=EXCLUDED.value_string, source=EXCLUDED.source, timestamp=EXCLUDED.timestamp,
			environment=EXCLUDED.environment, comment=EXCLUDED.comment, metadata=EXCLUDED.metadata,
			config_ref=EXCLUDED.config_ref, is_deleted=EXCLUDED.is_deleted, event_ts=EXCLUDED.event_ts,
			doc=EXCLUDED.doc, provenance=EXCLUDED.provenance, updated_at=now()`,
		projectID, id, c.subjectType, c.subjectID, c.name, c.dataType,
		c.valueNumeric, c.valueString, c.source, c.timestamp, c.environment, c.comment,
		c.metadata, c.configRef, c.isDeleted, ev.EventTS.UTC(), doc, provJSON)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetScore returns a folded score doc by id, or nil if absent/deleted.
func (s *Store) GetScore(ctx context.Context, projectID, id string) (json.RawMessage, error) {
	var doc []byte
	err := s.pool.QueryRow(ctx,
		`SELECT doc FROM scores WHERE project_id=$1 AND id=$2 AND is_deleted=false`,
		projectID, id).Scan(&doc)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return doc, err
}

// QueryScores runs a compiled scores predicate and returns folded score docs.
func (s *Store) QueryScores(ctx context.Context, where string, args []any, order string, limit int) ([]json.RawMessage, error) {
	sql := `SELECT doc FROM scores WHERE is_deleted=false`
	if where != "" {
		sql += " AND (" + where + ")"
	}
	if order != "" {
		sql += " ORDER BY " + order
	}
	sql += " LIMIT " + itoa(limit)
	rows, done, err := s.queryRead(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer done()
	budget := storage.NewResponseBudget(ctx)
	var out []json.RawMessage
	for rows.Next() {
		var doc []byte
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		if err := budget.Add(len(doc)); err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, rows.Err()
}

type scoreColumns struct {
	subjectType, subjectID, name, dataType *string
	valueString, source, environment       *string
	comment                                *string
	valueNumeric                           *float64
	timestamp                              *time.Time
	metadata, configRef                    []byte
	isDeleted                              bool
}

func extractScoreColumns(m map[string]any) scoreColumns {
	c := scoreColumns{
		subjectType:  strPtr(m, "subject_type"),
		subjectID:    strPtr(m, "subject_id"),
		name:         strPtr(m, "name"),
		dataType:     strPtr(m, "data_type"),
		valueString:  strPtr(m, "value_string"),
		source:       strPtr(m, "source"),
		environment:  strPtr(m, "environment"),
		comment:      strPtr(m, "comment"),
		valueNumeric: floatPtr(m, "value_numeric"),
		timestamp:    timePtr(m, "timestamp"),
		metadata:     jsonbOr(m, "metadata", "{}"),
		configRef:    jsonbOrNull(m, "config_ref"),
	}
	if b, ok := m["is_deleted"].(bool); ok {
		c.isDeleted = b
	}
	return c
}
