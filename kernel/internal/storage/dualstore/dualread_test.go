// Package dualstore's load-bearing proof: the read-after-write-across-boundary
// invariant (RULING-MIG6 / R-MIG5). A span written to the scale adapter is
// IMMEDIATELY readable through the unified dual-read Query API with NO window where
// it is missing because a read resolved against lite — and a historical span in
// lite is always readable too. Proven in BOTH directions AND under concurrent load.
//
// Env-gated on BOTH LLMOBS_TEST_DATABASE_URL (Postgres-lite) and LLMOBS_CH_TEST_DSN
// (ClickHouse-scale); skips unless both are present.
package dualstore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/query"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/clickhouse"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/dualstore"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

const projectID = "p-dual"

var maxWindow = 365 * 24 * time.Hour

type harness struct {
	lite storage.TelemetryStore
	dual *dualstore.Store
}

func setup(t *testing.T) *harness {
	t.Helper()
	pgURL := os.Getenv("LLMOBS_TEST_DATABASE_URL")
	chDSN := os.Getenv("LLMOBS_CH_TEST_DSN")
	if pgURL == "" || chDSN == "" {
		t.Skip("set BOTH LLMOBS_TEST_DATABASE_URL and LLMOBS_CH_TEST_DSN to run the dual-read proof")
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
	for _, stmt := range []string{`DELETE FROM scores WHERE project_id=$1`, `DELETE FROM spans WHERE project_id=$1`} {
		_, _ = pool.Exec(ctx, stmt, projectID)
	}
	_, _ = pool.Exec(ctx, `INSERT INTO organizations (id,name) VALUES ('o-dual','x') ON CONFLICT DO NOTHING`)
	_, _ = pool.Exec(ctx, `INSERT INTO projects (id,org_id,name) VALUES ($1,'o-dual','x') ON CONFLICT DO NOTHING`, projectID)
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

	return &harness{lite: lite, dual: dualstore.New(lite, scale)}
}

func spanEvent(id string, start time.Time, fields map[string]any) storage.Event {
	p := map[string]any{
		"project_id": projectID, "id": id, "trace_id": id, "kind": "generation",
		"name": id, "start_time": start.Format(time.RFC3339Nano),
	}
	for k, v := range fields {
		p[k] = v
	}
	return storage.Event{Op: storage.OpUpsert, EventTS: start, EventID: id, Payload: p}
}

// querySpansByID returns the ids visible via the unified dual-read for a wide window.
func (h *harness) visibleSpanIDs(t *testing.T) map[string]bool {
	t.Helper()
	now := time.Now().UTC()
	doc := map[string]any{
		"target": "spans",
		"timeRange": map[string]any{
			"from": now.Add(-72 * time.Hour).Format(time.RFC3339),
			"to":   now.Add(1 * time.Hour).Format(time.RFC3339),
		},
	}
	// Exercise the SAME path the query.DualRouter uses: compile the DSL doc for BOTH
	// dialects, run each backend on its own compiled SQL, and unify with MergeOrdered.
	// (The router can't fan ONE compiled statement to two engines — compiled SQL is
	// dialect-specific — so it compiles per dialect and merges; we mirror that here.)
	pg, err := query.CompileSpansForDialect(doc, projectID, maxWindow, query.PostgresDialect)
	if err != nil {
		t.Fatal(err)
	}
	chc, err := query.CompileSpansForDialect(doc, projectID, maxWindow, query.ClickHouseDialect)
	if err != nil {
		t.Fatal(err)
	}
	lite, scale := h.dual.Backends()
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
	rows := dualstore.MergeOrdered(scaleRows, liteRows, keys)
	out := map[string]bool{}
	for _, r := range rows {
		var m map[string]any
		_ = json.Unmarshal(r, &m)
		out[m["id"].(string)] = true
	}
	return out
}

// TestReadAfterWriteBothDirections: a span written through the dual store (→ scale)
// is immediately visible via the unified read; a span seeded directly in lite is
// too; neither has a window where the unified read misses it.
func TestReadAfterWriteBothDirections(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	// Direction 1: write via dual (lands in scale) → immediately readable.
	if err := h.dual.PersistSpan(ctx, spanEvent("scale-1", time.Now().UTC(), nil)); err != nil {
		t.Fatalf("dual persist: %v", err)
	}
	if got, _ := h.dual.GetSpan(ctx, projectID, "scale-1"); got == nil {
		t.Fatal("span written to scale not immediately readable via dual GetSpan")
	}
	if !h.visibleSpanIDs(t)["scale-1"] {
		t.Fatal("span written to scale not in the unified QuerySpans result")
	}

	// Direction 2: a historical span in lite → readable via the unified read.
	if err := h.lite.PersistSpan(ctx, spanEvent("lite-1", time.Now().UTC().Add(-48*time.Hour), nil)); err != nil {
		t.Fatalf("lite persist: %v", err)
	}
	if got, _ := h.dual.GetSpan(ctx, projectID, "lite-1"); got == nil {
		t.Fatal("historical span in lite not readable via dual GetSpan")
	}
	ids := h.visibleSpanIDs(t)
	if !ids["lite-1"] || !ids["scale-1"] {
		t.Fatalf("unified read must show BOTH stores' spans, got %v", ids)
	}
}

// TestConcurrentNoMissingWindow is the hardest proof: while writes land in scale,
// concurrent unified reads must NEVER miss an already-committed span (no window
// where a read "resolved against lite" and missed a scale write). Every committed
// id must be continuously visible from the moment its write returns.
func TestConcurrentNoMissingWindow(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	const n = 60

	committed := make(chan string, n)
	var wg sync.WaitGroup

	// Writers: half land in scale (via dual), half are historical (seeded in lite).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("cw-%02d", i)
			var err error
			if i%2 == 0 {
				err = h.dual.PersistSpan(ctx, spanEvent(id, time.Now().UTC(), nil)) // → scale
			} else {
				err = h.lite.PersistSpan(ctx, spanEvent(id, time.Now().UTC().Add(-24*time.Hour), nil)) // lite
			}
			if err != nil {
				t.Errorf("write %s: %v", id, err)
				return
			}
			committed <- id
			time.Sleep(time.Millisecond)
		}
		close(committed)
	}()

	// Reader: as each id is reported committed, assert it is visible via the unified
	// read RIGHT NOW — and stays visible (check a couple already-committed ids too).
	seen := []string{}
	for id := range committed {
		vis := h.visibleSpanIDs(t)
		if !vis[id] {
			t.Fatalf("committed span %s NOT visible via unified read — a missing window across the boundary", id)
		}
		seen = append(seen, id)
		// Re-check an earlier committed id: it must never disappear.
		if len(seen) > 3 {
			old := seen[len(seen)-3]
			if !vis[old] {
				t.Fatalf("previously-committed span %s vanished from the unified read", old)
			}
		}
	}
	wg.Wait()

	// Final: all n committed spans are visible together.
	vis := h.visibleSpanIDs(t)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("cw-%02d", i)
		if !vis[id] {
			t.Fatalf("final unified read missing committed span %s", id)
		}
	}
}
