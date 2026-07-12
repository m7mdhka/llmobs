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
// [from, to), writes a TTL-bounded suppression tombstone per erased span, and
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
// re-run re-resolves and retries; the DELETE only runs once the tombstones
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
	settings, err := s.readGuard()
	if err != nil {
		return 0, "", err
	}
	// apply_deleted_mask=1 (via readGuard) means the resolver already excludes
	// previously-erased rows, so a re-run after a partial erase resolves only what
	// remains — never re-issuing a delete for a row that is already gone.
	rows, err := s.conn.Query(ctx,
		`SELECT id FROM (SELECT * FROM spans WHERE project_id = ? ORDER BY ver DESC LIMIT 1 BY project_id, id)
		 WHERE user_id = ? AND start_time >= ? AND start_time < ?`+settings,
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
		// 1) Suppression tombstones — written BEFORE the delete so redelivery
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

	// 2) Irreversible physical removal — every version of each matched key.
	// BOUNDED + CHUNKED: a single DELETE with an unbounded `IN (?)` list is a
	// long synchronous mutation that can socket-hang-up the client (and a naive retry
	// then stacks a duplicate mutation over the still-running one). Deleting in bounded
	// chunks — each carrying the execution-time cap — keeps every statement bounded, and
	// because the resolver excludes already-erased rows a resumed run never re-deletes.
	// lightweight_deletes_sync stays at the server default (wait) so the erasure is
	// durable before EraseSpans returns — the GDPR guarantee is completion, not fire-and-
	// forget. Suppression (step 1) precedes the delete so a crash mid-delete can never
	// leave a resurrectable span; the audit (step 3) follows it so the audit attests a
	// COMPLETED physical erasure, never one that has not yet happened (or failed).
	for start := 0; start < count; start += eraseDeleteChunk {
		end := start + eraseDeleteChunk
		if end > count {
			end = count
		}
		if err := s.conn.Exec(ctx,
			`DELETE FROM spans WHERE project_id = ? AND id IN (?)`+s.mutationSettings(),
			projectID, ids[start:end]); err != nil {
			return 0, "", fmt.Errorf("delete erased spans [%d,%d): %w", start, end, err)
		}
	}

	// 3) Audit proof row LAST — written only once the physical removal succeeded, so a
	// durable audit never over-attests. A crash after the delete but before the audit
	// leaves the rows erased + suppressed (safe); a re-run re-resolves what remains and
	// audits that.
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
	return count, auditID, nil
}

// eraseDeleteChunk bounds how many ids a single erasure DELETE statement carries,
// so a large GDPR erasure is a sequence of bounded mutations, never one unbounded one.
const eraseDeleteChunk = 1000

// mutationSettings caps the erasure DELETE's wall-clock so a pathological mutation is
// refused by the server rather than hanging the client socket. Uses the same
// execution-time budget as reads; empty when unset (fail-open only on the cap, never on
// the delete itself — an unset cap is a misconfig surfaced elsewhere, not here).
func (s *Store) mutationSettings() string {
	if s.limits.MaxExecutionTime <= 0 {
		return ""
	}
	return fmt.Sprintf(" SETTINGS max_execution_time=%d", int64(s.limits.MaxExecutionTime.Seconds()))
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
