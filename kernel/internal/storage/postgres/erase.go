package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// EraseSpans hard-deletes every span for (projectID, userID) within [from, to),
// records an erasure-suppression tombstone for each deleted span id, and writes a
// proof-of-erasure audit row — all in one transaction. Returns the number erased
// and the audit id. Hard delete (not the soft is_deleted tombstone) because GDPR
// erasure must actually remove the payloads, not merely hide them; the suppression
// tombstones (G3) then ensure a later re-delivery cannot resurrect an erased span.
func (s *Store) EraseSpans(ctx context.Context, projectID, userID, actor string, from, to time.Time) (int, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	auditID, err := randomHexID("era")
	if err != nil {
		return 0, "", err
	}

	// DELETE ... RETURNING the erased ids so each becomes a suppression tombstone.
	rows, err := tx.Query(ctx,
		`DELETE FROM spans WHERE project_id=$1 AND user_id=$2 AND start_time >= $3 AND start_time < $4 RETURNING id`,
		projectID, userID, from.UTC(), to.UTC())
	if err != nil {
		return 0, "", err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, "", err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	count := len(ids)

	// One TTL-bounded suppression tombstone per erased span (set-based insert). The
	// persist path refuses a re-delivered span whose key matches an unexpired row,
	// so redelivery cannot undo the erasure.
	if count > 0 {
		expires := time.Now().Add(s.suppressionTTL).UTC()
		if _, err := tx.Exec(ctx,
			`INSERT INTO erasure_suppression (project_id, id, audit_id, expires_at)
			 SELECT $1, x.id, $2, $3 FROM unnest($4::text[]) AS x(id)
			 ON CONFLICT (project_id, id) DO UPDATE SET
			   audit_id=EXCLUDED.audit_id, erased_at=now(),
			   expires_at=GREATEST(erasure_suppression.expires_at, EXCLUDED.expires_at)`,
			projectID, auditID, expires, ids); err != nil {
			return 0, "", err
		}
	}

	filter, _ := json.Marshal(map[string]any{
		"user_id": userID,
		"from":    from.UTC().Format(time.RFC3339),
		"to":      to.UTC().Format(time.RFC3339),
	})
	if _, err := tx.Exec(ctx,
		`INSERT INTO erasure_audit (id, project_id, actor, filter, row_count) VALUES ($1,$2,$3,$4,$5)`,
		auditID, projectID, actor, filter, count); err != nil {
		return 0, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, "", err
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
