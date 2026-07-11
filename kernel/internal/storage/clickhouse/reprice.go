package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// SpansForReprice returns up to limit DERIVED span docs (settled, non-deleted, carrying a
// pricing_snapshot_ref) matching the filter, in the (start_time, project_id, id) TOTAL
// order the re-pricing runner resumes from — the same keyset the lite adapter uses, so a
// run produces identical results on either engine (cross-adapter parity). The dedup
// subquery (ORDER BY ver DESC LIMIT 1 BY project_id, id) selects the latest version, then
// is_deleted is filtered AFTER dedup so a delete tombstone hides its span.
//
// Unlike the lite scan there is no dead-letter NOT EXISTS: the reprice_deadletter table is
// control-plane (Postgres), not ClickHouse. It is unneeded here — the cursor advances past
// every scanned span after its batch, so a dead-lettered anomaly is stepped over and never
// re-fetched; a crash-replay of the same batch merely re-dead-letters it idempotently.
func (s *Store) SpansForReprice(ctx context.Context, runKey string, f storage.RepriceFilter, after storage.RepriceCursor, limit int) ([]json.RawMessage, storage.RepriceCursor, error) {
	settings, err := s.readGuard()
	if err != nil {
		return nil, after, err
	}
	ts := after.TS
	if ts.IsZero() {
		ts = time.Unix(0, 0).UTC()
	}

	// A derived span carries a non-empty pricing_snapshot_ref.id; a provided-cost span
	// (R1) has an empty ref and is never scanned. Placeholders are positional in
	// ClickHouse, so bind in the order the `?` appear: filter first, then the keyset.
	var where strings.Builder
	where.WriteString("is_deleted = 0 AND pricing_snapshot_ref != '' AND JSONExtractString(pricing_snapshot_ref, 'id') != ''")
	var bind []any
	if f.SnapshotRefID != "" {
		where.WriteString(" AND JSONExtractString(pricing_snapshot_ref, 'id') = ?")
		bind = append(bind, f.SnapshotRefID)
	}
	if f.ProjectID != "" {
		where.WriteString(" AND project_id = ?")
		bind = append(bind, f.ProjectID)
	}
	// Row-value keyset on (start_time, project_id, id) — ClickHouse tuple comparison is
	// the total-ordered strict advance (same-timestamp spans never loop).
	where.WriteString(" AND (start_time, project_id, id) > (?, ?, ?)")
	bind = append(bind, ts, after.ProjectID, after.ID)

	sql := "SELECT doc, start_time, project_id, id FROM (" +
		"SELECT * FROM spans ORDER BY ver DESC LIMIT 1 BY project_id, id" +
		") WHERE " + where.String() +
		" ORDER BY start_time ASC, project_id ASC, id ASC LIMIT " + strconv.Itoa(limit) + settings

	rows, err := s.conn.Query(ctx, sql, bind...)
	if err != nil {
		return nil, after, fmt.Errorf("clickhouse reprice scan: %w", err)
	}
	defer rows.Close()

	var out []json.RawMessage
	next := after
	for rows.Next() {
		var (
			doc       string
			startTime time.Time
			projectID string
			id        string
		)
		if err := rows.Scan(&doc, &startTime, &projectID, &id); err != nil {
			return nil, after, fmt.Errorf("clickhouse reprice scan row: %w", err)
		}
		out = append(out, json.RawMessage(doc))
		next = storage.RepriceCursor{TS: startTime.UTC(), ProjectID: projectID, ID: id}
	}
	return out, next, rows.Err()
}
