package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// TestSuppressedIdNotReadableEvenIfReinserted is the Postgres symmetric read-side #77
// proof (mirrors the ClickHouse TestIntegrationSuppressedIdNotReadableEvenIfReinserted):
// a non-deleted span that carries an unexpired suppression tombstone — the outcome of a
// concurrent erase whose tombstone commits AFTER a re-ingest/backfill insert races past
// the write-side NOT EXISTS guard — must be excluded from every read. Without the
// read-side exclusion the row (is_deleted=false) would be readable: a resurrected erased
// span.
func TestSuppressedIdNotReadableEvenIfReinserted(t *testing.T) {
	url := os.Getenv("LLMOBS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set LLMOBS_TEST_DATABASE_URL to run")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM spans WHERE project_id=$1`, "p-supp")
	_, _ = pool.Exec(ctx, `DELETE FROM erasure_suppression WHERE project_id=$1`, "p-supp")
	_, _ = pool.Exec(ctx, `INSERT INTO organizations (id,name) VALUES ('o-supp','x') ON CONFLICT DO NOTHING`)
	_, _ = pool.Exec(ctx, `INSERT INTO projects (id,org_id,name) VALUES ('p-supp','o-supp','x') ON CONFLICT DO NOTHING`)
	// Clean up our spans + tombstones so a shared test DB isn't left with a tombstone that
	// would (correctly) make a later re-emit/reprice of a colliding id dead-letter.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM spans WHERE project_id=$1`, "p-supp")
		_, _ = pool.Exec(context.Background(), `DELETE FROM erasure_suppression WHERE project_id=$1`, "p-supp")
	})

	s := NewStore(pool)
	start := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	ev := storage.Event{Op: storage.OpUpsert, EventTS: start, EventID: "race1-1", Payload: map[string]any{
		"project_id": "p-supp", "id": "race1", "trace_id": "tr", "kind": "generation", "name": "gen",
		"start_time": start.Format(time.RFC3339Nano), "user_id": "u1", "input": "SECRET-race1",
	}}
	if err := s.PersistSpan(ctx, ev); err != nil {
		t.Fatalf("persist: %v", err)
	}
	// The tombstone appears AFTER the row landed (the race outcome the write guard misses).
	if _, err := pool.Exec(ctx,
		`INSERT INTO erasure_suppression (project_id, id, audit_id, expires_at) VALUES ($1,$2,$3,$4)`,
		"p-supp", "race1", "aud", time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	// Precondition: the row is present and NOT soft-deleted, so only the read-side
	// suppression exclusion can be what hides it.
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM spans WHERE project_id='p-supp' AND id='race1' AND is_deleted=false`).Scan(&n); err != nil {
		t.Fatalf("precondition: %v", err)
	}
	if n == 0 {
		t.Fatal("precondition: the reinserted row must be present and non-deleted")
	}

	// Every read must exclude it.
	if doc, _ := s.GetSpan(ctx, "p-supp", "race1"); doc != nil {
		t.Fatalf("GetSpan returned a suppressed-but-reinserted span — resurrection: %s", doc)
	}
	tspans, err := s.GetTraceSpans(ctx, "p-supp", "tr")
	if err != nil {
		t.Fatalf("GetTraceSpans: %v", err)
	}
	if len(tspans) != 0 {
		t.Fatalf("GetTraceSpans returned %d spans for a fully-suppressed trace — resurrection", len(tspans))
	}
	spans, err := s.QuerySpans(ctx, "project_id = $1", []any{"p-supp"}, "", 100)
	if err != nil {
		t.Fatalf("QuerySpans: %v", err)
	}
	for _, r := range spans {
		if strings.Contains(string(r), "race1") {
			t.Fatalf("QuerySpans returned a suppressed-but-reinserted span: %s", r)
		}
	}
}
