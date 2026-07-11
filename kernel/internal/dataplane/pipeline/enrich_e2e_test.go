package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/normalize"
	"github.com/m7mdhka/llmobs/kernel/internal/pricing"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

// TestEnrichEndToEndCostQueryable is the M2 ACCEPTANCE bar (the capability gap closed):
// an instrumented OTLP trace, ingested through the FULL pipeline (decode → normalize →
// enrich → persist) against a real Postgres with a seeded price table, has total_cost
// PRESENT and CORRECT when read back through the storage/Query path — cost is derived
// once at ingest and stored, not computed at read time. Env-gated on LLMOBS_TEST_DATABASE_URL.
func TestEnrichEndToEndCostQueryable(t *testing.T) {
	url := os.Getenv("LLMOBS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set LLMOBS_TEST_DATABASE_URL to run the cost end-to-end acceptance test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pg: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_, _ = pool.Exec(ctx, `INSERT INTO organizations (id,name) VALUES ('o-e2e','x') ON CONFLICT DO NOTHING`)
	_, _ = pool.Exec(ctx, `INSERT INTO projects (id,org_id,name) VALUES ('p-e2e','o-e2e','x') ON CONFLICT DO NOTHING`)
	_, _ = pool.Exec(ctx, `DELETE FROM spans WHERE project_id='p-e2e'`)
	// Append-only price entries accumulate versions across runs; clear this model so the
	// seeded version is deterministically 1.
	_, _ = pool.Exec(ctx, `DELETE FROM price_entries WHERE provider='openai' AND model='gpt-4o'`)

	store := postgres.NewStore(pool)
	prices := postgres.NewPriceStore(pool)
	// Seed a known price so the expected cost is exact: input $10/tok, output $20/tok,
	// cache_read $1/tok reducing input (absurd round rates for exact arithmetic).
	if _, err := prices.Upsert(ctx, pricing.Entry{
		Provider: "openai", Model: "gpt-4o", EffectiveFrom: "2020-01-01T00:00:00Z",
		Rates: map[string]pricing.Rate{
			"input": {PerToken: 10}, "output": {PerToken: 20}, "cache_read": {PerToken: 1, Reduces: "input"},
		},
	}, "test"); err != nil {
		t.Fatalf("seed price: %v", err)
	}

	pipe := New(pool, store, normalize.Default(), NoopBus{}, Config{Prices: prices})

	// An instrumented chat span: 1000 input (200 cached), 500 output.
	body := otlpChat(t, "openai", "gpt-4o", 1000, 500, 200)
	ing := &Ingestion{
		ContentType: "application/json", Body: body, ReceivedAt: time.Now(),
		Identity: controlplane.Identity{ProjectID: "p-e2e"},
	}
	if err := pipe.RunPreauth(ctx, ing); err != nil {
		t.Fatalf("pipeline: %v", err)
	}

	// Read the span back through the storage/Query path and assert derived cost.
	doc, err := store.GetSpan(ctx, "p-e2e", "5350e2e100000001")
	if err != nil || doc == nil {
		t.Fatalf("get span: %v (doc=%v)", err, doc)
	}
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatal(err)
	}
	if m["cost_source"] != "derived" {
		t.Fatalf("cost_source=%v want derived", m["cost_source"])
	}
	// input residual 1000−200=800 → 8000; cache_read 200 → 200; output 500 → 10000. Total 18200.
	if tc, _ := m["total_cost"].(float64); tc != 18200.0 {
		t.Fatalf("total_cost through the query path = %v, want 18200 (the capability gap: cost present + correct)", tc)
	}
	ref, _ := m["pricing_snapshot_ref"].(map[string]any)
	if ref["id"] != "openai/gpt-4o#1" {
		t.Fatalf("pricing_snapshot_ref must record the price entry, got %v", ref)
	}
}

// otlpChat builds a minimal OTLP/JSON trace with one chat generation span carrying usage.
func otlpChat(t *testing.T, provider, model string, input, output, cacheRead int64) []byte {
	t.Helper()
	attr := func(k string, v any) map[string]any {
		switch x := v.(type) {
		case string:
			return map[string]any{"key": k, "value": map[string]any{"stringValue": x}}
		case int64:
			return map[string]any{"key": k, "value": map[string]any{"intValue": itoaJSON(x)}}
		}
		return nil
	}
	span := map[string]any{
		"traceId": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "spanId": "5350e2e100000001", "name": "chat", "kind": 3,
		"startTimeUnixNano": "1720000000000000000", "endTimeUnixNano": "1720000001000000000",
		"attributes": []any{
			attr("gen_ai.operation.name", "chat"),
			attr("gen_ai.request.model", model),
			attr("gen_ai.provider.name", provider),
			attr("gen_ai.usage.input_tokens", input),
			attr("gen_ai.usage.output_tokens", output),
			attr("gen_ai.usage.input_cached_tokens", cacheRead),
		},
	}
	doc := map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": []any{}},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "e2e"}, "spans": []any{span}}},
	}}}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func itoaJSON(n int64) string {
	b, _ := json.Marshal(n)
	// OTLP intValue is a string-encoded int64.
	return string(b)
}
