package backfill_test

// Integration proof for the resumable backfill against BOTH real engines: settled
// Postgres-lite rows are copied into ClickHouse-scale and become readable there. This
// exercises the real (ts, project_id, id) keyset SQL (row-value comparison + COALESCE)
// — the piece a fake source can't validate — and the #7117 same-timestamp cluster on
// a real engine. Env-gated on LLMOBS_TEST_DATABASE_URL + LLMOBS_CH_TEST_DSN.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/query"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/backfill"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/clickhouse"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/dualstore"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

const projectID = "p-backfill"

var maxWindow = 365 * 24 * time.Hour

// setupEngines migrates + wipes both real engines and returns the lite (Postgres) and
// scale (ClickHouse) stores. Skips unless both DSNs are set.
func setupEngines(t *testing.T) (*postgres.Store, *clickhouse.Store) {
	t.Helper()
	pgURL := os.Getenv("LLMOBS_TEST_DATABASE_URL")
	chDSN := os.Getenv("LLMOBS_CH_TEST_DSN")
	if pgURL == "" || chDSN == "" {
		t.Skip("set BOTH LLMOBS_TEST_DATABASE_URL and LLMOBS_CH_TEST_DSN to run the backfill integration proof")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, pgURL)
	if err != nil {
		t.Fatalf("pg: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("pg migrate: %v", err)
	}
	// Backfill scans the WHOLE lite store (all projects), so wipe every span/score for
	// a deterministic count, not just this project's.
	_, _ = pool.Exec(ctx, `DELETE FROM scores`)
	_, _ = pool.Exec(ctx, `DELETE FROM spans`)
	_, _ = pool.Exec(ctx, `DELETE FROM erasure_suppression`)
	_, _ = pool.Exec(ctx, `DELETE FROM erasure_audit`)
	_, _ = pool.Exec(ctx, `DELETE FROM backfill_state`)
	_, _ = pool.Exec(ctx, `DELETE FROM backfill_deadletter`)
	_, _ = pool.Exec(ctx, `INSERT INTO organizations (id,name) VALUES ('o-bf','x') ON CONFLICT DO NOTHING`)
	_, _ = pool.Exec(ctx, `INSERT INTO projects (id,org_id,name) VALUES ($1,'o-bf','x') ON CONFLICT DO NOTHING`, projectID)
	lite := postgres.NewStore(pool)

	opts, err := ch.ParseDSN(chDSN)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := ch.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, tbl := range []string{"spans", "scores", "erasure_suppression", "erasure_audit", "schema_migrations"} {
		_ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+tbl)
	}
	if err := clickhouse.Migrate(ctx, conn, clickhouse.Config{}); err != nil {
		t.Fatalf("ch migrate: %v", err)
	}
	scale := clickhouse.NewStore(conn)
	scale.SetReadLimits(clickhouse.ReadLimits{
		MaxExecutionTime: 30 * time.Second, MaxMemoryUsage: 2 << 30,
		MaxRowsToRead: 50_000_000, MaxBytesToRead: 5 << 30,
	})
	return lite, scale
}

// unifiedSpanIDs runs the SAME dual-read the query.DualRouter runs: compile the DSL
// for BOTH dialects, query each backend, MergeOrdered the union. It is the unified
// view a self-hoster sees — the "never stranded" surface.
func unifiedSpanIDs(t *testing.T, lite *postgres.Store, scale *clickhouse.Store) map[string]bool {
	t.Helper()
	now := time.Now().UTC()
	doc := map[string]any{
		"target": "spans",
		"timeRange": map[string]any{
			"from": now.Add(-72 * time.Hour).Format(time.RFC3339),
			"to":   now.Add(1 * time.Hour).Format(time.RFC3339),
		},
	}
	pg, err := query.CompileSpansForDialect(doc, projectID, maxWindow, query.PostgresDialect)
	if err != nil {
		t.Fatal(err)
	}
	chc, err := query.CompileSpansForDialect(doc, projectID, maxWindow, query.ClickHouseDialect)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	liteRows, err := lite.QuerySpans(ctx, pg.Where, pg.Args, pg.Order, 1000)
	if err != nil {
		t.Fatalf("lite QuerySpans: %v", err)
	}
	scaleRows, err := scale.QuerySpans(ctx, chc.Where, chc.Args, chc.Order, 1000)
	if err != nil {
		t.Fatalf("scale QuerySpans: %v", err)
	}
	var keys []dualstore.OrderKey
	for _, k := range pg.OrderKeys {
		keys = append(keys, dualstore.OrderKey{Field: k.Field, Desc: k.Desc})
	}
	out := map[string]bool{}
	for _, r := range dualstore.MergeOrdered(scaleRows, liteRows, keys) {
		var m map[string]any
		_ = json.Unmarshal(r, &m)
		if id, ok := m["id"].(string); ok {
			out[id] = true
		}
	}
	return out
}

// TestNeverStrandDualReadThenBackfill is the positioning conformance proof: a
// self-hoster mid-migration (historical data in lite, new data in scale) is NEVER
// stranded — the unified read shows everything BEFORE any backfill — and the optional
// backfill then moves the historical rows onto scale without ever losing visibility.
func TestNeverStrandDualReadThenBackfill(t *testing.T) {
	lite, scale := setupEngines(t)
	ctx := context.Background()

	// Historical data: 10 spans in LITE only (as if written before cutover).
	shared := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	hist := map[string]bool{}
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("hist-%02d", i)
		hist[id] = true
		if err := lite.PersistSpan(ctx, spanEvent(id, shared.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("seed lite %s: %v", id, err)
		}
	}
	// New data: 10 spans written through the dual store (land in SCALE).
	dual := dualstore.New(lite, scale)
	newer := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("new-%02d", i)
		if err := dual.PersistSpan(ctx, spanEvent(id, newer.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("dual persist %s: %v", id, err)
		}
	}

	// BEFORE backfill: the unified read must already show ALL 20 — nothing stranded.
	pre := unifiedSpanIDs(t, lite, scale)
	for i := 0; i < 10; i++ {
		if !pre[fmt.Sprintf("hist-%02d", i)] || !pre[fmt.Sprintf("new-%02d", i)] {
			t.Fatalf("pre-backfill unified read stranded data: %v", pre)
		}
	}

	// Run the OPTIONAL backfill: move lite's historical rows onto scale.
	r := backfill.New(lite, scale, backfill.Config{ChunkSize: 4, BackoffBase: time.Millisecond}, quietLog())
	if _, err := r.Run(ctx); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// AFTER backfill: still all 20 unified, AND every historical row is now directly in
	// scale (the backfill moved cold data onto the scale engine, no visibility lost).
	post := unifiedSpanIDs(t, lite, scale)
	for id := range hist {
		if !post[id] {
			t.Fatalf("post-backfill unified read lost historical %s", id)
		}
		doc, err := scale.GetSpan(ctx, projectID, id)
		if err != nil || doc == nil {
			t.Fatalf("historical %s not on scale after backfill (err=%v)", id, err)
		}
	}
}

// TestErasedLiteSpanNotResurrectedByBackfill is the regression fixture for the
// cross-boundary resurrection gap the adversarial review found: a span present ONLY
// in lite, once erased through the dual store, must NOT reappear in scale when the
// backfill later runs — because the dual erase pre-suppresses lite's ids in scale.
func TestErasedLiteSpanNotResurrectedByBackfill(t *testing.T) {
	lite, scale := setupEngines(t)
	ctx := context.Background()

	// A historical span in LITE only, owned by the user we will erase.
	ts := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	ev := spanEvent("erase-me", ts)
	ev.Payload["user_id"] = "u-erase"
	if err := lite.PersistSpan(ctx, ev); err != nil {
		t.Fatalf("seed lite: %v", err)
	}

	// Erase through the DUAL store (as the Query API does). Scale holds nothing for
	// this id, so without the guard it would write no tombstone.
	dual := dualstore.New(lite, scale)
	if _, _, err := dual.EraseSpans(ctx, projectID, "u-erase", "session:x", ts.Add(-time.Hour), ts.Add(time.Hour)); err != nil {
		t.Fatalf("dual erase: %v", err)
	}

	// The span is gone from lite (hard delete); re-seed it into lite to simulate the
	// backfill holding an already-read copy (or any lite→scale replay attempt).
	if err := lite.PersistSpan(ctx, ev); err != nil {
		// lite's own suppression tombstone should block re-persist; that's fine — the
		// point is scale must never receive it. Ignore a suppression rejection here.
		_ = err
	}

	// Attempt the replay the backfill would do: persist the settled lite doc into scale.
	if err := scale.PersistSpan(ctx, ev); err == nil {
		// PersistSpan may return nil (suppressed as a no-op) — that's fine. What matters
		// is the row must NOT be readable in scale.
	}
	if doc, err := scale.GetSpan(ctx, projectID, "erase-me"); err != nil {
		t.Fatalf("scale GetSpan: %v", err)
	} else if doc != nil {
		t.Fatal("ERASED lite-only span was resurrected into scale — the suppression guard failed")
	}
}

func TestBackfillLiteToScaleReadable(t *testing.T) {
	lite, scale := setupEngines(t)
	ctx := context.Background()

	// Seed lite: 20 spans, HALF sharing one exact timestamp (the #7117 cluster).
	shared := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	want := map[string]bool{}
	for i := 0; i < 20; i++ {
		ts := shared
		if i >= 10 {
			ts = shared.Add(time.Duration(i) * time.Minute)
		}
		id := fmt.Sprintf("bf-%02d", i)
		want[id] = true
		if err := lite.PersistSpan(ctx, spanEvent(id, ts)); err != nil {
			t.Fatalf("seed lite %s: %v", id, err)
		}
	}

	// Run backfill with small chunks (forces many keyset hops through the cluster).
	r := backfill.New(lite, scale, backfill.Config{ChunkSize: 4, BackoffBase: time.Millisecond}, quietLog())
	res, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("backfill run: %v", err)
	}
	if res.SpansMigrated != 20 || !res.Complete {
		t.Fatalf("want 20 migrated + complete, got migrated=%d complete=%v", res.SpansMigrated, res.Complete)
	}

	// Every seeded span must now be readable directly from SCALE.
	for id := range want {
		doc, err := scale.GetSpan(ctx, projectID, id)
		if err != nil {
			t.Fatalf("scale GetSpan %s: %v", id, err)
		}
		if doc == nil {
			t.Fatalf("span %s not readable in scale after backfill (data lost across the boundary)", id)
		}
	}
}

func spanEvent(id string, start time.Time) storage.Event {
	p := map[string]any{
		"project_id": projectID, "id": id, "trace_id": id, "kind": "generation",
		"name": id, "start_time": start.Format(time.RFC3339Nano),
	}
	return storage.Event{Op: storage.OpUpsert, EventTS: start, EventID: id, Payload: p}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
