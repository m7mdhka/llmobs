package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// BackfillCursor is the TOTAL order a lite→scale backfill resumes from:
// (ts, project_id, id). The tuple is fully ordering — two projects sharing a
// timestamp+id still order deterministically — so a keyset scan strictly advances
// and same-timestamp rows can never loop (a non-strict cursor that revisits a
// same-timestamp cluster forever is the classic infinite-loop trap). The zero
// cursor (TS zero, empty ids) starts from the very beginning.
type BackfillCursor struct {
	TS        time.Time
	ProjectID string
	ID        string
}

// BackfillSpans returns up to limit settled span docs whose (start_time, project_id,
// id) is strictly greater than after, ascending, with the next cursor. NULL
// start_time coalesces to epoch so it sorts first and is still totally ordered.
// Deleted rows are skipped (erased data is never migrated). The read runs on the
// caller's ctx — the backfill gives it its OWN generous budget, NOT the interactive
// statement timeout (a short interactive timeout is exactly what broke v4's backfill).
func (s *Store) BackfillSpans(ctx context.Context, after BackfillCursor, limit int) ([]json.RawMessage, BackfillCursor, error) {
	return s.backfillScan(ctx, "spans", "COALESCE(start_time, 'epoch'::timestamptz)", after, limit)
}

// BackfillScores mirrors BackfillSpans over the scores table (timestamp anchor).
func (s *Store) BackfillScores(ctx context.Context, after BackfillCursor, limit int) ([]json.RawMessage, BackfillCursor, error) {
	return s.backfillScan(ctx, "scores", "timestamp", after, limit)
}

func (s *Store) backfillScan(ctx context.Context, table, tsExpr string, after BackfillCursor, limit int) ([]json.RawMessage, BackfillCursor, error) {
	// Row-value keyset on (ts, project_id, id): strictly-greater tuple comparison is
	// the total-ordered advance. Bind the cursor ts (epoch for the zero cursor so the
	// first page starts at the beginning).
	ts := after.TS
	if ts.IsZero() {
		ts = time.Unix(0, 0).UTC()
	}
	// Exclude rows already recorded in backfill_deadletter ($4 = kind): a
	// deterministically-failing row is dead-lettered once and then stepped over on every
	// subsequent scan, so it can never be re-fetched and re-fail — guaranteeing forward
	// progress even for a contiguous cluster of bad rows (belt-and-suspenders alongside
	// the cursor advance, which already skips past them).
	// table is a fixed internal constant ("spans"/"scores"), so reference it by name
	// (no alias) — tsExpr references its bare columns (e.g. start_time), which resolve
	// against it directly.
	sql := fmt.Sprintf(
		`SELECT %[1]s AS anchor_ts, %[2]s.project_id, %[2]s.id, %[2]s.doc
		   FROM %[2]s
		  WHERE %[2]s.is_deleted = false
		    AND (%[1]s, %[2]s.project_id, %[2]s.id) > ($1, $2, $3)
		    AND NOT EXISTS (
		          SELECT 1 FROM backfill_deadletter dl
		           WHERE dl.kind = $4 AND dl.project_id = %[2]s.project_id AND dl.id = %[2]s.id)
		  ORDER BY %[1]s ASC, %[2]s.project_id ASC, %[2]s.id ASC
		  LIMIT %[3]d`, tsExpr, table, limit)
	rows, err := s.pool.Query(ctx, sql, ts, after.ProjectID, after.ID, table)
	if err != nil {
		return nil, after, fmt.Errorf("backfill scan %s: %w", table, err)
	}
	defer rows.Close()

	var out []json.RawMessage
	next := after
	for rows.Next() {
		var (
			anchorTS  time.Time
			projectID string
			id        string
			doc       []byte
		)
		if err := rows.Scan(&anchorTS, &projectID, &id, &doc); err != nil {
			return nil, after, fmt.Errorf("backfill scan %s row: %w", table, err)
		}
		out = append(out, doc)
		next = BackfillCursor{TS: anchorTS.UTC(), ProjectID: projectID, ID: id}
	}
	if err := rows.Err(); err != nil {
		return nil, after, fmt.Errorf("backfill scan %s rows: %w", table, err)
	}
	return out, next, nil
}

// LoadBackfillState reads the persisted resume cursor for a kind ('spans'|'scores').
// Returns the cursor, whether the kind is fully done, and how many rows migrated.
func (s *Store) LoadBackfillState(ctx context.Context, kind string) (cur BackfillCursor, done bool, migrated int64, err error) {
	var (
		ts   *time.Time
		proj string
		id   string
	)
	row := s.pool.QueryRow(ctx,
		`SELECT cursor_ts, cursor_proj, cursor_id, done, migrated FROM backfill_state WHERE kind=$1`, kind)
	switch e := row.Scan(&ts, &proj, &id, &done, &migrated); e {
	case nil:
		if ts != nil {
			cur.TS = ts.UTC()
		}
		cur.ProjectID, cur.ID = proj, id
		return cur, done, migrated, nil
	case pgx.ErrNoRows:
		return BackfillCursor{}, false, 0, nil
	default:
		return BackfillCursor{}, false, 0, fmt.Errorf("load backfill state %s: %w", kind, e)
	}
}

// SaveBackfillState upserts the resume cursor + progress for a kind. Called after
// every batch so a restart resumes from the last durably-migrated row.
func (s *Store) SaveBackfillState(ctx context.Context, kind string, cur BackfillCursor, done bool, migrated int64) error {
	var ts *time.Time
	if !cur.TS.IsZero() {
		t := cur.TS.UTC()
		ts = &t
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO backfill_state (kind, cursor_ts, cursor_proj, cursor_id, done, migrated, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6, now())
		 ON CONFLICT (kind) DO UPDATE SET
		   cursor_ts=EXCLUDED.cursor_ts, cursor_proj=EXCLUDED.cursor_proj, cursor_id=EXCLUDED.cursor_id,
		   done=EXCLUDED.done, migrated=EXCLUDED.migrated, updated_at=now()`,
		kind, ts, cur.ProjectID, cur.ID, done, migrated)
	if err != nil {
		return fmt.Errorf("save backfill state %s: %w", kind, err)
	}
	return nil
}

// DeadLetterBackfill records a PERMANENTLY-failing record so the backfill can skip it
// and make progress. A permanent failure (malformed record, a deterministic rejection)
// must dead-letter rather than retry forever — retrying forever would pin the run and
// either loop indefinitely or force a data-dropping shortcut. The row is retained for
// audit; it is never silently dropped and never retried forever.
func (s *Store) DeadLetterBackfill(ctx context.Context, kind, projectID, id, reason string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO backfill_deadletter (kind, project_id, id, reason, at)
		 VALUES ($1,$2,$3,$4, now())
		 ON CONFLICT (kind, project_id, id) DO UPDATE SET reason=EXCLUDED.reason, at=now()`,
		kind, projectID, id, reason)
	if err != nil {
		return fmt.Errorf("dead-letter backfill %s/%s: %w", kind, id, err)
	}
	return nil
}
