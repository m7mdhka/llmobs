package clickhouse

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// dialCH connects to a real ClickHouse from LLMOBS_CH_TEST_DSN, or skips. Kept
// env-gated (not build-tagged) so the test always compiles in CI; it runs only
// where a ClickHouse is provisioned (the scale e2e / a local `make ch-up`).
func dialCH(t *testing.T) driver.Conn {
	t.Helper()
	dsn := os.Getenv("LLMOBS_CH_TEST_DSN")
	if dsn == "" {
		t.Skip("LLMOBS_CH_TEST_DSN unset — skipping real-ClickHouse integration test")
	}
	opts, err := ch.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	conn, err := ch.Open(opts)
	if err != nil {
		t.Fatalf("open clickhouse: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		t.Fatalf("ping clickhouse: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// freshSchema migrates a clean standalone schema, dropping the tables first so
// re-runs are hermetic.
func freshSchema(t *testing.T, conn driver.Conn) {
	t.Helper()
	ctx := context.Background()
	for _, tbl := range []string{"spans", "scores", "erasure_suppression", "erasure_audit", "schema_migrations"} {
		if err := conn.Exec(ctx, "DROP TABLE IF EXISTS "+tbl); err != nil {
			t.Fatalf("drop %s: %v", tbl, err)
		}
	}
	if err := Migrate(ctx, conn, Config{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// R-CH2: a second Migrate over the same schema is a no-op (idempotent).
	if err := Migrate(ctx, conn, Config{}); err != nil {
		t.Fatalf("second migrate (idempotency): %v", err)
	}
}

func up(ts int, id string, payload map[string]any) storage.Event {
	p := map[string]any{"project_id": "p", "id": id}
	for k, v := range payload {
		p[k] = v
	}
	return storage.Event{
		Op:      storage.OpUpsert,
		EventTS: time.Unix(int64(ts), 0).UTC(),
		EventID: id + "-" + string(rune('0'+ts)),
		Payload: p,
	}
}

func TestIntegrationMigrateAndMerge(t *testing.T) {
	conn := dialCH(t)
	freshSchema(t, conn)
	ctx := context.Background()
	s := NewStore(conn)

	// Two out-of-order events for the same (project_id, id): merge-on-write must
	// converge to the union, exactly as lite folds — proving the shared fold drives
	// the real write path.
	start := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.PersistSpan(ctx, up(2, "s1", map[string]any{
		"kind": "generation", "start_time": start, "name": "second",
		"attributes": map[string]any{"y": 2.0},
	})); err != nil {
		t.Fatalf("persist span (ts2): %v", err)
	}
	if err := s.PersistSpan(ctx, up(1, "s1", map[string]any{
		"kind": "generation", "start_time": start, "name": "first",
		"attributes": map[string]any{"x": 1.0},
	})); err != nil {
		t.Fatalf("persist span (ts1): %v", err)
	}

	state, _, err := s.readSettled(ctx, "spans", "p", "s1")
	if err != nil {
		t.Fatalf("read settled: %v", err)
	}
	// name is last-writer-wins by event_ts: ts2 ("second") wins over the later-
	// arriving-but-older ts1 ("first").
	if got := state["name"]; got != "second" {
		t.Errorf("name = %v, want \"second\" (higher event_ts wins)", got)
	}
	// attributes deep-merge across both events.
	attrs, _ := state["attributes"].(map[string]any)
	if attrs["x"] != 1.0 || attrs["y"] != 2.0 {
		t.Errorf("attributes = %v, want both x=1 and y=2 (deep merge)", attrs)
	}
}

func TestIntegrationPathologicalQueryCapped(t *testing.T) {
	conn := dialCH(t)
	freshSchema(t, conn)
	ctx := context.Background()
	s := NewStore(conn)

	// Seed a handful of spans.
	start := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < 20; i++ {
		id := "sp" + string(rune('a'+i))
		if err := s.PersistSpan(ctx, up(1, id, map[string]any{"kind": "generation", "start_time": start})); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// A pathological cap: at most 1 row may be scanned. The query MUST be rejected
	// by ClickHouse (capped), not run to completion — and the connection must stay
	// usable afterward (a cap protects the cluster; it does not take it down).
	s.SetReadLimits(ReadLimits{
		MaxExecutionTime: 30 * time.Second,
		MaxMemoryUsage:   1 << 30,
		MaxRowsToRead:    1,
		MaxBytesToRead:   1 << 30,
	})
	_, err := s.QuerySpans(ctx, "project_id = ?", []any{"p"}, "", 1000)
	if err == nil {
		t.Fatal("expected the row-cap to reject the pathological scan, got nil error")
	}
	if !strings.Contains(err.Error(), "rows") && !strings.Contains(err.Error(), "limit") && !strings.Contains(strings.ToLower(err.Error()), "exceed") {
		t.Fatalf("expected a rows-limit error, got: %v", err)
	}

	// The cluster survived: a sane-limit query on the same connection works.
	s.SetReadLimits(ReadLimits{
		MaxExecutionTime: 30 * time.Second,
		MaxMemoryUsage:   1 << 30,
		MaxRowsToRead:    10_000_000,
		MaxBytesToRead:   1 << 30,
	})
	rows, err := s.QuerySpans(ctx, "project_id = ?", []any{"p"}, "", 1000)
	if err != nil {
		t.Fatalf("post-cap query failed (connection poisoned?): %v", err)
	}
	if len(rows) != 20 {
		t.Fatalf("post-cap query returned %d rows, want 20", len(rows))
	}
}

func TestIntegrationPreflightPasses(t *testing.T) {
	conn := dialCH(t)
	// The test user (DEFAULT_ACCESS_MANAGEMENT) holds the full grant set, so the
	// functional preflight must pass cleanly against a real node.
	if err := Preflight(context.Background(), conn, "llmobs"); err != nil {
		t.Fatalf("preflight against fully-granted user failed: %v", err)
	}
}

func TestIntegrationErasureSuppression(t *testing.T) {
	conn := dialCH(t)
	freshSchema(t, conn)
	ctx := context.Background()
	s := NewStore(conn)

	// Write an unexpired tombstone for (p, s2), then a re-delivery must be
	// suppressed (G3) — not resurrected.
	if err := conn.Exec(ctx,
		"INSERT INTO erasure_suppression (project_id, id, audit_id, expires_at, event_ts) VALUES (?, ?, ?, ?, ?)",
		"p", "s2", "aud1", time.Now().UTC().Add(time.Hour), time.Now().UTC()); err != nil {
		t.Fatalf("insert tombstone: %v", err)
	}
	// Forgery case (the security-review regression guard): a re-delivered erased
	// span carrying a FUTURE event timestamp must STILL be suppressed. Expiry is
	// evaluated against server time, never the attacker-controlled event ts — else
	// a forged 2099 timestamp would skip suppression and resurrect erased data.
	forged := up(1, "s2", map[string]any{
		"kind": "generation", "start_time": time.Now().UTC().Format(time.RFC3339Nano), "name": "resurrected?",
	})
	forged.EventTS = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC) // far past the 1h tombstone expiry
	err := s.PersistSpan(ctx, forged)
	if !errors.Is(err, storage.ErrSuppressedByErasure) {
		t.Fatalf("expected ErrSuppressedByErasure for future-dated forged event, got %v", err)
	}
	// And nothing was written.
	state, _, rerr := s.readSettled(ctx, "spans", "p", "s2")
	if rerr != nil {
		t.Fatalf("read settled: %v", rerr)
	}
	if len(state) != 0 {
		t.Errorf("suppressed span must not be stored, got %v", state)
	}
}
