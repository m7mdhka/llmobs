package clickhouse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// EraseSpans physically removes every settled span for (projectID, userID) within
// [from, to), writes a TTL-bounded suppression tombstone per erased span (G3), and
// records an erasure-audit row. Returns the count erased and the audit id.
//
// GDPR erasure must remove the payload, not merely hide it, so this deletes the
// rows (ClickHouse lightweight DELETE — rows become invisible immediately and are
// physically dropped on the next merge) rather than writing an is_deleted
// tombstone (which would retain the doc). The suppression tombstones then prevent
// a later re-delivery from resurrecting an erased span.
//
// ClickHouse has no transactions, so the steps are ORDERED so the irreversible
// physical DELETE is LAST: resolve ids → write suppression tombstones → write
// audit → DELETE. If any pre-DELETE step fails the rows are still present and a
// re-run re-resolves and retries; the DELETE only runs once the tombstones (G3)
// and audit are durable, so a matched span can never be erased-without-suppression
// (which would let a re-delivery resurrect it).
func (s *Store) EraseSpans(ctx context.Context, projectID, userID, actor string, from, to time.Time) (int, string, error) {
	auditID, err := randomHexID("era")
	if err != nil {
		return 0, "", err
	}

	// Resolve the settled spans matching the filter — INCLUDING soft-deleted ones:
	// a soft-delete tombstone retains the payload, and older physical versions hold
	// the raw prompt/completion, so GDPR erasure must remove them regardless of
	// is_deleted state. Erasure keys on (project_id, id) so ALL physical versions of
	// a matched span are removed even if a stale version carried a different
	// (mutable) user_id.
	rows, err := s.conn.Query(ctx,
		`SELECT id FROM (SELECT * FROM spans WHERE project_id = ? ORDER BY ver DESC LIMIT 1 BY project_id, id)
		 WHERE user_id = ? AND start_time >= ? AND start_time < ?`+s.limits.settings(),
		projectID, userID, from.UTC(), to.UTC())
	if err != nil {
		return 0, "", fmt.Errorf("resolve erasure ids: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, "", fmt.Errorf("scan erasure id: %w", err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	count := len(ids)

	if count > 0 {
		// 1) Suppression tombstones (G3) — written BEFORE the delete so redelivery
		// can never resurrect an erased span even if the process dies mid-erase.
		expires := time.Now().Add(s.suppressionTTL).UTC()
		batch, err := s.conn.PrepareBatch(ctx,
			`INSERT INTO erasure_suppression (project_id, id, audit_id, expires_at, event_ts)`)
		if err != nil {
			return 0, "", fmt.Errorf("prepare suppression batch: %w", err)
		}
		now := time.Now().UTC()
		for _, id := range ids {
			if err := batch.Append(projectID, id, auditID, expires, now); err != nil {
				return 0, "", fmt.Errorf("append suppression: %w", err)
			}
		}
		if err := batch.Send(); err != nil {
			return 0, "", fmt.Errorf("send suppression batch: %w", err)
		}
	}

	// 2) Audit proof row.
	filter, _ := json.Marshal(map[string]any{
		"user_id": userID,
		"from":    from.UTC().Format(time.RFC3339),
		"to":      to.UTC().Format(time.RFC3339),
	})
	ab, err := s.conn.PrepareBatch(ctx,
		`INSERT INTO erasure_audit (id, project_id, actor, filter, row_count, created_at)`)
	if err != nil {
		return 0, "", fmt.Errorf("prepare audit batch: %w", err)
	}
	if err := ab.Append(auditID, projectID, actor, string(filter), uint64(count), time.Now().UTC()); err != nil {
		return 0, "", fmt.Errorf("append audit: %w", err)
	}
	if err := ab.Send(); err != nil {
		return 0, "", fmt.Errorf("send audit: %w", err)
	}

	// 3) Irreversible physical removal LAST — every version of each matched key.
	if count > 0 {
		if err := s.conn.Exec(ctx,
			`DELETE FROM spans WHERE project_id = ? AND id IN (?)`, projectID, ids); err != nil {
			return 0, "", fmt.Errorf("delete erased spans: %w", err)
		}
	}
	return count, auditID, nil
}

// SuppressSpans records an erasure-suppression tombstone for each explicit id without
// deleting. Used by the dual-read store to make scale's suppression set complete
// across the boundary: a span erased from lite-only must also be suppressed in scale
// so the backfill/seed cannot resurrect it (scale.PersistSpan checks this table).
func (s *Store) SuppressSpans(ctx context.Context, projectID string, ids []string, auditID string) error {
	if len(ids) == 0 {
		return nil
	}
	expires := time.Now().Add(s.suppressionTTL).UTC()
	batch, err := s.conn.PrepareBatch(ctx,
		`INSERT INTO erasure_suppression (project_id, id, audit_id, expires_at, event_ts)`)
	if err != nil {
		return fmt.Errorf("prepare suppression batch: %w", err)
	}
	now := time.Now().UTC()
	for _, id := range ids {
		if err := batch.Append(projectID, id, auditID, expires, now); err != nil {
			return fmt.Errorf("append suppression: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send suppression batch: %w", err)
	}
	return nil
}

func randomHexID(prefix string) (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(buf), nil
}
