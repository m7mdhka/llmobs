package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestStatementTimeoutCancelsSlowQuery proves the K1.5 backstop against a REAL
// Postgres: a query that outruns the configured statement_timeout is cancelled
// server-side in ~the timeout, not left to run to completion. Needs no schema
// (pg_sleep only), so it runs against any Postgres.
//
// Run it against the dev-profile Postgres:
//
//	docker compose -f deploy/compose/dev.yaml up -d
//	LLMOBS_TEST_DATABASE_URL="postgres://llmobs:llmobs@localhost:5432/llmobs?sslmode=disable" \
//	  go test ./internal/storage/postgres/ -run StatementTimeout -v
func TestStatementTimeoutCancelsSlowQuery(t *testing.T) {
	url := os.Getenv("LLMOBS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set LLMOBS_TEST_DATABASE_URL to run (e.g. the deploy/compose/dev.yaml Postgres on :5432)")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer pool.Close()
	s := NewStore(pool)

	// With no timeout, a short sleep completes normally (the fallback path).
	s.SetQueryTimeout(0)
	rows, done, err := s.queryRead(context.Background(), "SELECT pg_sleep(0.2)")
	if err != nil {
		t.Fatalf("no-timeout query should succeed: %v", err)
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("no-timeout query errored: %v", err)
	}
	done()

	// With a tiny timeout, a long sleep is cancelled server-side FAST — proving the
	// timeout is enforced by Postgres, not merely by the client giving up.
	s.SetQueryTimeout(50 * time.Millisecond)
	start := time.Now()
	rows2, done2, qerr := s.queryRead(context.Background(), "SELECT pg_sleep(5)")
	if qerr == nil {
		for rows2.Next() {
		}
		qerr = rows2.Err()
		done2()
	}
	elapsed := time.Since(start)
	if qerr == nil {
		t.Fatal("expected a statement_timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("slow query ran %v — statement_timeout not enforced server-side", elapsed)
	}
	t.Logf("slow query cancelled in %v: %v", elapsed, qerr)
}
