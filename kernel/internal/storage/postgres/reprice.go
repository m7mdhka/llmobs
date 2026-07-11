package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// SpansForReprice returns up to limit DERIVED span docs (non-deleted, carrying a
// pricing_snapshot_ref) matching the filter, whose (start_time, project_id, id) is
// strictly greater than after, ascending, with the next cursor. The keyset is the same
// TOTAL order the L5 backfill uses so a same-timestamp cluster never loops (#7117).
// Spans already dead-lettered for this run are excluded, so a permanent per-span anomaly
// is stepped over once and never re-fetched. The caller runs this on its OWN generous
// budget, never the interactive statement timeout.
func (s *Store) SpansForReprice(ctx context.Context, runKey string, f storage.RepriceFilter, after storage.RepriceCursor, limit int) ([]json.RawMessage, storage.RepriceCursor, error) {
	ts := after.TS
	if ts.IsZero() {
		ts = time.Unix(0, 0).UTC()
	}
	// Bound params only — the filter values (a price-version id, a project id) are always
	// bound, never concatenated. A derived span is exactly one carrying a non-empty
	// pricing_snapshot_ref.id; a provided-cost span (R1) has a null ref and is never
	// scanned, so provided cost is untouchable by re-pricing by construction.
	var where strings.Builder
	where.WriteString(`is_deleted = false
		    AND pricing_snapshot_ref IS NOT NULL
		    AND COALESCE(pricing_snapshot_ref->>'id','') <> ''`)
	args := []any{ts, after.ProjectID, after.ID, runKey}
	next := 5
	if f.SnapshotRefID != "" {
		where.WriteString(fmt.Sprintf(" AND pricing_snapshot_ref->>'id' = $%d", next))
		args = append(args, f.SnapshotRefID)
		next++
	}
	if f.ProjectID != "" {
		where.WriteString(fmt.Sprintf(" AND project_id = $%d", next))
		args = append(args, f.ProjectID)
		next++
	}
	sql := fmt.Sprintf(`
		SELECT COALESCE(start_time, 'epoch'::timestamptz) AS anchor_ts, project_id, id, doc
		  FROM spans
		 WHERE %s
		   AND (COALESCE(start_time, 'epoch'::timestamptz), project_id, id) > ($1, $2, $3)
		   AND NOT EXISTS (
		         SELECT 1 FROM reprice_deadletter dl
		          WHERE dl.run_key = $4 AND dl.project_id = spans.project_id AND dl.id = spans.id)
		 ORDER BY anchor_ts ASC, project_id ASC, id ASC
		 LIMIT %d`, where.String(), limit)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, after, fmt.Errorf("reprice scan: %w", err)
	}
	defer rows.Close()

	var out []json.RawMessage
	nextCur := after
	for rows.Next() {
		var (
			anchorTS  time.Time
			projectID string
			id        string
			doc       []byte
		)
		if err := rows.Scan(&anchorTS, &projectID, &id, &doc); err != nil {
			return nil, after, fmt.Errorf("reprice scan row: %w", err)
		}
		out = append(out, doc)
		nextCur = storage.RepriceCursor{TS: anchorTS.UTC(), ProjectID: projectID, ID: id}
	}
	if err := rows.Err(); err != nil {
		return nil, after, fmt.Errorf("reprice scan rows: %w", err)
	}
	return out, nextCur, nil
}

// LoadRepriceState reads the in-flight resume cursor + progress for a run. No row (the
// common case, and always after a completed run's ClearRepriceState) means start fresh.
func (s *Store) LoadRepriceState(ctx context.Context, runKey string) (cur storage.RepriceCursor, repriced, scanned int64, err error) {
	var (
		ts   *time.Time
		proj string
		id   string
	)
	row := s.pool.QueryRow(ctx,
		`SELECT cursor_ts, cursor_proj, cursor_id, repriced, scanned FROM reprice_state WHERE run_key=$1`, runKey)
	switch e := row.Scan(&ts, &proj, &id, &repriced, &scanned); e {
	case nil:
		if ts != nil {
			cur.TS = ts.UTC()
		}
		cur.ProjectID, cur.ID = proj, id
		return cur, repriced, scanned, nil
	case pgx.ErrNoRows:
		return storage.RepriceCursor{}, 0, 0, nil
	default:
		return storage.RepriceCursor{}, 0, 0, fmt.Errorf("load reprice state %s: %w", runKey, e)
	}
}

// SaveRepriceState upserts the resume cursor + progress after every batch (in-flight
// crash-resumption). The done column is retained in the schema but always false while a
// run is live — completion DELETEs the row via ClearRepriceState instead.
func (s *Store) SaveRepriceState(ctx context.Context, runKey string, cur storage.RepriceCursor, repriced, scanned int64) error {
	var ts *time.Time
	if !cur.TS.IsZero() {
		t := cur.TS.UTC()
		ts = &t
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO reprice_state (run_key, cursor_ts, cursor_proj, cursor_id, done, repriced, scanned, updated_at)
		 VALUES ($1,$2,$3,$4,false,$5,$6, now())
		 ON CONFLICT (run_key) DO UPDATE SET
		   cursor_ts=EXCLUDED.cursor_ts, cursor_proj=EXCLUDED.cursor_proj, cursor_id=EXCLUDED.cursor_id,
		   repriced=EXCLUDED.repriced, scanned=EXCLUDED.scanned, updated_at=now()`,
		runKey, ts, cur.ProjectID, cur.ID, repriced, scanned)
	if err != nil {
		return fmt.Errorf("save reprice state %s: %w", runKey, err)
	}
	return nil
}

// ClearRepriceState deletes a completed run's cursor row, so a later re-trigger starts
// fresh and re-scans (re-pricing is idempotent when nothing changed, and catches a
// subsequent change to the same run key — e.g. a second discount edit).
func (s *Store) ClearRepriceState(ctx context.Context, runKey string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM reprice_state WHERE run_key=$1`, runKey); err != nil {
		return fmt.Errorf("clear reprice state %s: %w", runKey, err)
	}
	return nil
}

// DeadLetterReprice records a span that PERMANENTLY fails to re-price (a data-integrity
// anomaly) so the run steps over it and makes progress (CLAUDE.md #12) — retained for
// audit, never silently dropped, never retried forever.
func (s *Store) DeadLetterReprice(ctx context.Context, runKey, projectID, id, reason string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO reprice_deadletter (run_key, project_id, id, reason, at)
		 VALUES ($1,$2,$3,$4, now())
		 ON CONFLICT (run_key, project_id, id) DO UPDATE SET reason=EXCLUDED.reason, at=now()`,
		runKey, projectID, id, reason)
	if err != nil {
		return fmt.Errorf("dead-letter reprice %s/%s: %w", runKey, id, err)
	}
	return nil
}
