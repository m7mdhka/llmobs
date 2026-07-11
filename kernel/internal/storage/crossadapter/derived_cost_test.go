package crossadapter

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// TestCrossAdapterDerivedCostParity is the M2 money-path cross-adapter proof: a span
// carrying a HIGH-PRECISION derived cost (computed once in Go at enrich, stored
// verbatim) reads back IDENTICALLY from Postgres and ClickHouse. Cost is a fixed-scale
// decimal (§1, Decimal64(12) on ClickHouse); the documented bound is equality to 12
// fractional digits. Derivation runs once at ingest — both adapters persist the same
// float64 — so any divergence here is a storage-representation bug, not arithmetic.
func TestCrossAdapterDerivedCostParity(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	// A realistic derived total with many significant digits + a cost_details breakdown.
	const total = 0.000123456789012
	ev := storage.Event{
		Op: storage.OpUpsert, EventTS: ts(9), EventID: "cost1",
		Payload: map[string]any{
			"project_id": projectID, "id": "cost1", "trace_id": "tc", "kind": "generation",
			"name": "chat", "start_time": ts(9).Format(time.RFC3339Nano), "end_time": ts(10).Format(time.RFC3339Nano),
			"model": "gpt-4o", "provider": "openai",
			"usage_details":        map[string]any{"input": int64(1000), "output": int64(500), "cache_read": int64(200)},
			"cost_details":         map[string]any{"input": 0.00008, "output": 0.00004, "cache_read": 0.000003456789012},
			"total_cost":           total,
			"cost_source":          "derived",
			"pricing_snapshot_ref": map[string]any{"type": "price", "id": "openai/gpt-4o#1", "label": "openai/gpt-4o v1"},
		},
	}
	if err := h.pg.PersistSpan(ctx, ev); err != nil {
		t.Fatalf("pg persist: %v", err)
	}
	if err := h.ch.PersistSpan(ctx, ev); err != nil {
		t.Fatalf("ch persist: %v", err)
	}

	pgDoc, err := h.pg.GetSpan(ctx, projectID, "cost1")
	if err != nil || pgDoc == nil {
		t.Fatalf("pg get: %v", err)
	}
	chDoc, err := h.ch.GetSpan(ctx, projectID, "cost1")
	if err != nil || chDoc == nil {
		t.Fatalf("ch get: %v", err)
	}
	pgCost := costOf(t, pgDoc)
	chCost := costOf(t, chDoc)
	// Equality within the Decimal64(12) bound.
	if math.Abs(pgCost-chCost) > 1e-12 {
		t.Fatalf("derived total_cost diverges across adapters: pg=%.15g ch=%.15g", pgCost, chCost)
	}
	// And both match the stored value to the documented 12-digit bound.
	if math.Abs(pgCost-total) > 1e-12 || math.Abs(chCost-total) > 1e-12 {
		t.Fatalf("total_cost not preserved to 12 digits: pg=%.15g ch=%.15g want=%.15g", pgCost, chCost, total)
	}
}

func costOf(t *testing.T, doc []byte) float64 {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatal(err)
	}
	c, ok := m["total_cost"].(float64)
	if !ok {
		t.Fatalf("total_cost missing/not a number: %v", m["total_cost"])
	}
	return c
}
