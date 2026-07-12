package crossadapter

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/costderive"
	"github.com/m7mdhka/llmobs/kernel/internal/pricing"
	"github.com/m7mdhka/llmobs/kernel/internal/reprice"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/clickhouse"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

// TestReprice_CrossAdapterParity proves the re-pricing cross-adapter guarantee: a price correction
// re-priced independently against the Postgres scanner and the ClickHouse scanner lands
// the SAME new cost and the SAME new snapshot ref on both engines. The derivation is the
// shared costderive path (store-independent), and the re-emit folds identically, so the
// two engines cannot diverge. The run state is Postgres in both cases (control-plane).
func TestReprice_CrossAdapterParity(t *testing.T) {
	pgURL := os.Getenv("LLMOBS_TEST_DATABASE_URL")
	chDSN := os.Getenv("LLMOBS_CH_TEST_DSN")
	if pgURL == "" || chDSN == "" {
		t.Skip("set BOTH LLMOBS_TEST_DATABASE_URL and LLMOBS_CH_TEST_DSN to run the cross-adapter re-price parity proof")
	}
	ctx := context.Background()

	// --- Postgres store + price store + clean slate ---
	pool, err := pgxpool.New(ctx, pgURL)
	if err != nil {
		t.Fatalf("pg connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("pg migrate: %v", err)
	}
	_, _ = pool.Exec(ctx, `INSERT INTO organizations (id, name) VALUES ('o-rpx','x') ON CONFLICT DO NOTHING`)
	_, _ = pool.Exec(ctx, `INSERT INTO projects (id, org_id, name) VALUES ('rpx','o-rpx','x') ON CONFLICT DO NOTHING`)
	for _, stmt := range []string{
		`DELETE FROM spans WHERE project_id='rpx'`,
		`DELETE FROM price_entries WHERE provider='openai' AND model='gpt-4o'`,
		`DELETE FROM reprice_state WHERE run_key LIKE '%rpx%' OR run_key LIKE '%openai/gpt-4o%'`,
		`DELETE FROM reprice_deadletter WHERE run_key LIKE '%openai/gpt-4o%'`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("pg clean: %v", err)
		}
	}
	pgStore := postgres.NewStore(pool)
	priceStore := postgres.NewPriceStore(pool)

	// --- ClickHouse store + clean slate ---
	opts, err := ch.ParseDSN(chDSN)
	if err != nil {
		t.Fatalf("ch dsn: %v", err)
	}
	conn, err := ch.Open(opts)
	if err != nil {
		t.Fatalf("ch open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, tbl := range []string{"spans", "scores", "erasure_suppression", "erasure_audit", "schema_migrations"} {
		if err := conn.Exec(ctx, "DROP TABLE IF EXISTS "+tbl); err != nil {
			t.Fatalf("ch drop %s: %v", tbl, err)
		}
	}
	if err := clickhouse.Migrate(ctx, conn, clickhouse.Config{}); err != nil {
		t.Fatalf("ch migrate: %v", err)
	}
	chStore := clickhouse.NewStore(conn)
	chStore.SetReadLimits(clickhouse.ReadLimits{
		MaxExecutionTime: 30 * time.Second, MaxMemoryUsage: 2 << 30,
		MaxRowsToRead: 50_000_000, MaxBytesToRead: 5 << 30,
	})

	// v1 price, then ingest a derived span into BOTH engines via the shared derivation.
	if _, err := priceStore.Upsert(ctx, pricing.Entry{
		Provider: "openai", Model: "gpt-4o", EffectiveFrom: "2026-01-01T00:00:00Z",
		Rates: map[string]pricing.Rate{"input": {PerToken: 0.0000025}, "output": {PerToken: 0.00001}},
	}, "session:admin@x"); err != nil {
		t.Fatalf("upsert v1: %v", err)
	}
	span := map[string]any{
		"project_id": "rpx", "id": "x1", "trace_id": "tx1", "kind": "generation",
		"model": "gpt-4o", "provider": "openai", "start_time": "2026-06-01T12:00:00Z",
		"provided_usage_details": map[string]any{"input": 1000.0, "output": 500.0},
	}
	discount, _, _ := priceStore.GetDiscount(ctx, "rpx")
	if _, err := costderive.DeriveSpanCost(ctx, span, priceStore, discount, map[string]*pricing.Entry{}); err != nil {
		t.Fatalf("seed derive: %v", err)
	}
	ingest := storage.Event{Op: storage.OpUpsert, EventTS: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), EventID: "x1-ingest", Payload: span}
	if err := pgStore.PersistSpan(ctx, ingest); err != nil {
		t.Fatalf("pg seed: %v", err)
	}
	if err := chStore.PersistSpan(ctx, ingest); err != nil {
		t.Fatalf("ch seed: %v", err)
	}
	_ = conn.Exec(ctx, "OPTIMIZE TABLE spans FINAL")

	// Price correction → v2.
	if _, err := priceStore.Upsert(ctx, pricing.Entry{
		Provider: "openai", Model: "gpt-4o", EffectiveFrom: "2026-01-01T00:00:00Z",
		Rates: map[string]pricing.Rate{"input": {PerToken: 0.000002}, "output": {PerToken: 0.000008}},
	}, "session:admin@x"); err != nil {
		t.Fatalf("upsert v2: %v", err)
	}

	fixed := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := reprice.Config{ChunkSize: 10, Now: func() time.Time { return fixed }}
	// Scope to this test's project so the proof is isolated from other crossadapter tests'
	// spans in the shared DB (a global price re-price would legitimately scan every
	// project's matching spans; here we only want to compare the two engines).
	filter := storage.RepriceFilter{SnapshotRefID: "openai/gpt-4o#1", ProjectID: "rpx"}

	// Re-price each engine independently (distinct KeyPrefix so their Postgres cursors
	// don't collide), then compare the resulting cost byte-for-byte.
	pgCfg, chCfg := cfg, cfg
	pgCfg.KeyPrefix, chCfg.KeyPrefix = "lite:", "scale:"
	if res, err := reprice.New(pgStore, pgStore, priceStore, pgCfg, nil).Run(ctx, filter); err != nil || res.Repriced != 1 {
		t.Fatalf("pg reprice: res=%+v err=%v", res, err)
	}
	if res, err := reprice.New(chStore, pgStore, priceStore, chCfg, nil).Run(ctx, filter); err != nil || res.Repriced != 1 {
		t.Fatalf("ch reprice: res=%+v err=%v", res, err)
	}
	_ = conn.Exec(ctx, "OPTIMIZE TABLE spans FINAL")

	pgTotal, pgRef := readSpanCost(t, ctx, pgGet(pgStore, ctx))
	chTotal, chRef := readSpanCost(t, ctx, chGet(chStore, ctx))
	if pgTotal != 0.006 || pgRef != "openai/gpt-4o#2" {
		t.Fatalf("pg re-priced = %v / %q, want 0.006 / #2", pgTotal, pgRef)
	}
	// BYTE-IDENTICAL across engines: same derivation, same fold.
	if pgTotal != chTotal || pgRef != chRef {
		t.Fatalf("cross-adapter divergence: pg=%v/%q ch=%v/%q", pgTotal, pgRef, chTotal, chRef)
	}
}

func pgGet(s *postgres.Store, ctx context.Context) json.RawMessage {
	doc, _ := s.GetSpan(ctx, "rpx", "x1")
	return doc
}
func chGet(s *clickhouse.Store, ctx context.Context) json.RawMessage {
	doc, _ := s.GetSpan(ctx, "rpx", "x1")
	return doc
}

func readSpanCost(t *testing.T, _ context.Context, doc json.RawMessage) (float64, string) {
	t.Helper()
	if doc == nil {
		t.Fatal("span missing")
	}
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	total, _ := m["total_cost"].(float64)
	var ref string
	if r, ok := m["pricing_snapshot_ref"].(map[string]any); ok {
		ref, _ = r["id"].(string)
	}
	return total, ref
}
