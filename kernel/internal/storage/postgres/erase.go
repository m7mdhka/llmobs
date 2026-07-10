package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// EraseSpans hard-deletes every span for (projectID, userID) within [from, to)
// and records a proof-of-erasure audit row in the same transaction. Returns the
// number erased and the audit id. Hard delete (not the soft is_deleted tombstone)
// because GDPR erasure must actually remove the payloads, not merely hide them.
func (s *Store) EraseSpans(ctx context.Context, projectID, userID, actor string, from, to time.Time) (int, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`DELETE FROM spans WHERE project_id=$1 AND user_id=$2 AND start_time >= $3 AND start_time < $4`,
		projectID, userID, from.UTC(), to.UTC())
	if err != nil {
		return 0, "", err
	}
	count := int(tag.RowsAffected())

	auditID, err := randomHexID("era")
	if err != nil {
		return 0, "", err
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
