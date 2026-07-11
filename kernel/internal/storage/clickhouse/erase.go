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
// Unlike the lite adapter this is not one transaction (ClickHouse has none); the
// steps are ordered so a mid-failure re-run converges: ids are resolved first, the
// DELETE + tombstones + audit are each idempotent on the resolved id set.
func (s *Store) EraseSpans(ctx context.Context, projectID, userID, actor string, from, to time.Time) (int, string, error) {
	auditID, err := randomHexID("era")
	if err != nil {
		return 0, "", err
	}

	// Resolve the settled, non-deleted spans matching the filter. Erasure keys on
	// (project_id, id) so ALL physical versions of a matched span are removed even
	// if a stale version carried a different (mutable) user_id.
	rows, err := s.conn.Query(ctx,
		`SELECT id FROM (SELECT * FROM spans WHERE project_id = ? ORDER BY ver DESC LIMIT 1 BY project_id, id)
		 WHERE is_deleted = 0 AND user_id = ? AND start_time >= ? AND start_time < ?`,
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
		// Physically remove every version of the matched keys.
		if err := s.conn.Exec(ctx,
			`DELETE FROM spans WHERE project_id = ? AND id IN (?)`, projectID, ids); err != nil {
			return 0, "", fmt.Errorf("delete erased spans: %w", err)
		}

		// One suppression tombstone per erased id (G3).
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

func randomHexID(prefix string) (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(buf), nil
}
