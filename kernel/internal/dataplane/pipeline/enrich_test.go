package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/normalize"
	"github.com/m7mdhka/llmobs/kernel/internal/pricing"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// fakePrices is a table-backed PriceResolver recording how often Resolve is called
// (so R1's short-circuit can be proven: derivation never consults the table).
type fakePrices struct {
	entries      map[string]*pricing.Entry // key: provider\x00model
	discount     float64
	resolveCalls int
	resolveErr   error
}

func (f *fakePrices) Resolve(_ context.Context, provider, model string, _ time.Time) (*pricing.Entry, error) {
	f.resolveCalls++
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	return f.entries[pricing.CanonicalProvider(provider)+"\x00"+pricing.CanonicalModel(model)], nil
}
func (f *fakePrices) GetDiscount(context.Context, string) (float64, bool, error) {
	if f.discount > 0 {
		return f.discount, true, nil
	}
	return 0, false, nil
}

func run1(t *testing.T, prices PriceResolver, payload map[string]any) map[string]any {
	t.Helper()
	s := &enrichStage{prices: prices}
	ing := &Ingestion{
		Identity: controlplane.Identity{ProjectID: "p"},
		Events:   []storage.Event{{Op: storage.OpUpsert, Payload: payload}},
	}
	if err := s.Process(context.Background(), ing); err != nil {
		t.Fatalf("enrich: %v", err)
	}
	return ing.Events[0].Payload
}

// TestEnrichProvidedCostWins is R1: a client-supplied cost short-circuits derivation
// entirely — cost_details copies the provided map, cost_source=provided, no price entry
// is consulted, and pricing_snapshot_ref stays null.
func TestEnrichProvidedCostWins(t *testing.T) {
	prices := &fakePrices{entries: map[string]*pricing.Entry{
		"openai\x00gpt-4o": {ID: "openai/gpt-4o#1", Rates: map[string]pricing.Rate{"input": {PerToken: 1}}},
	}}
	out := run1(t, prices, map[string]any{
		"model": "gpt-4o", "provider": "openai", "start_time": "2026-01-01T00:00:00Z",
		"provided_usage_details": map[string]any{"input": int64(100)},
		"provided_cost_details":  map[string]any{"input": 0.5, "total": 0.5},
		"cost_source":            "provided",
		"total_cost":             0.5,
	})
	if out["cost_source"] != "provided" {
		t.Fatalf("cost_source=%v", out["cost_source"])
	}
	cd, _ := out["cost_details"].(map[string]any)
	if cd["input"] != 0.5 {
		t.Fatalf("cost_details must copy provided, got %v", cd)
	}
	if _, ok := out["pricing_snapshot_ref"]; ok {
		t.Fatal("provided cost must not set a pricing_snapshot_ref")
	}
	if prices.resolveCalls != 0 {
		t.Fatalf("provided cost must SHORT-CIRCUIT derivation; price table consulted %d times", prices.resolveCalls)
	}
}

// TestEnrichNoUsageNoCost is R4/§7.3: a model with NO usage must not fabricate cost —
// cost_details/total_cost/cost_source stay null, and no estimation happens.
func TestEnrichNoUsageNoCost(t *testing.T) {
	prices := &fakePrices{entries: map[string]*pricing.Entry{
		"openai\x00gpt-4o": {ID: "openai/gpt-4o#1", Rates: map[string]pricing.Rate{"input": {PerToken: 1}}},
	}}
	out := run1(t, prices, map[string]any{
		"model": "gpt-4o", "provider": "openai", "start_time": "2026-01-01T00:00:00Z",
	})
	for _, k := range []string{"cost_details", "total_cost", "cost_source", "usage_details"} {
		if _, ok := out[k]; ok {
			t.Fatalf("no usage → %q must be absent (no phantom cost), got %v", k, out[k])
		}
	}
}

// TestEnrichDerivesDataDriven is R2/R3: a Google/Gemini call (a provider NOT on any
// allow-list) with a cache_read bucket and a price entry carrying a cache rate → cache
// priced at the cache rate, input on its residual, cost_source=derived, snapshot ref set.
func TestEnrichDerivesDataDriven(t *testing.T) {
	prices := &fakePrices{entries: map[string]*pricing.Entry{
		"google\x00gemini-1.5-pro": {
			ID: "google/gemini-1.5-pro#1", Provider: "google", Model: "gemini-1.5-pro", Version: 1,
			Rates: map[string]pricing.Rate{
				"input":      {PerToken: 10},
				"output":     {PerToken: 20},
				"cache_read": {PerToken: 1, Reduces: "input"},
			},
		},
	}}
	out := run1(t, prices, map[string]any{
		"model": "vertex_ai/gemini-1.5-pro", "provider": "vertex_ai", // aliases → canonical google/gemini-1.5-pro (R6)
		"start_time":             "2026-01-01T00:00:00Z",
		"provided_usage_details": map[string]any{"input": int64(1000), "output": int64(500), "cache_read": int64(200)},
	})
	if out["cost_source"] != "derived" {
		t.Fatalf("cost_source=%v", out["cost_source"])
	}
	cd, _ := out["cost_details"].(map[string]any)
	// input residual = 1000 − 200 = 800 → 8000; cache_read = 200*1 = 200; output = 500*20 = 10000.
	if cd["input"] != 8000.0 || cd["cache_read"] != 200.0 || cd["output"] != 10000.0 {
		t.Fatalf("data-driven residual cost wrong: %v", cd)
	}
	if out["total_cost"] != 18200.0 {
		t.Fatalf("total_cost=%v want 18200", out["total_cost"])
	}
	ref, _ := out["pricing_snapshot_ref"].(map[string]any)
	if ref["type"] != "price" || ref["id"] != "google/gemini-1.5-pro#1" {
		t.Fatalf("snapshot ref wrong: %v", ref)
	}
}

// TestEnrichDiscountApplied proves the per-project discount multiplies derived cost.
func TestEnrichDiscountApplied(t *testing.T) {
	prices := &fakePrices{discount: 0.5, entries: map[string]*pricing.Entry{
		"openai\x00gpt-4o": {ID: "openai/gpt-4o#1", Provider: "openai", Model: "gpt-4o", Version: 1,
			Rates: map[string]pricing.Rate{"input": {PerToken: 10}}},
	}}
	out := run1(t, prices, map[string]any{
		"model": "gpt-4o", "provider": "openai", "start_time": "2026-01-01T00:00:00Z",
		"provided_usage_details": map[string]any{"input": int64(100)},
	})
	if out["total_cost"] != 500.0 { // 100*10*0.5
		t.Fatalf("discounted total_cost=%v want 500", out["total_cost"])
	}
}

// TestEnrichNoEntryLeavesNull: a model with usage but NO price entry → cost null (never
// a fabricated zero), and no snapshot ref.
func TestEnrichNoEntryLeavesNull(t *testing.T) {
	prices := &fakePrices{entries: map[string]*pricing.Entry{}}
	out := run1(t, prices, map[string]any{
		"model": "unknown-model", "provider": "openai", "start_time": "2026-01-01T00:00:00Z",
		"provided_usage_details": map[string]any{"input": int64(100)},
	})
	if _, ok := out["total_cost"]; ok {
		t.Fatal("no price entry → total_cost must be null, not zero")
	}
	if out["cost_source"] != nil {
		t.Fatalf("cost_source must be unset, got %v", out["cost_source"])
	}
	// usage_details IS resolved even without a price (it's provided).
	if out["usage_details"] == nil {
		t.Fatal("usage_details should resolve from provided even without a price")
	}
}

// TestEnrichFailSoft: a price-lookup error must NOT fail ingest — cost is left null.
func TestEnrichFailSoft(t *testing.T) {
	prices := &fakePrices{resolveErr: errors.New("db down"), entries: map[string]*pricing.Entry{}}
	s := &enrichStage{prices: prices}
	ing := &Ingestion{
		Identity: controlplane.Identity{ProjectID: "p"},
		Events: []storage.Event{{Payload: map[string]any{
			"model": "gpt-4o", "provider": "openai", "start_time": "2026-01-01T00:00:00Z",
			"provided_usage_details": map[string]any{"input": int64(100)},
		}}},
	}
	if err := s.Process(context.Background(), ing); err != nil {
		t.Fatalf("a price-lookup error must NOT fail ingest: %v", err)
	}
	if _, ok := ing.Events[0].Payload["total_cost"]; ok {
		t.Fatal("on lookup error, cost must be left null")
	}
}

// TestEnrichNilResolverNoop: no price table configured → enrich is a pass-through.
func TestEnrichNilResolverNoop(t *testing.T) {
	out := run1(t, nil, map[string]any{
		"model": "gpt-4o", "provider": "openai",
		"provided_usage_details": map[string]any{"input": int64(100)},
	})
	if _, ok := out["total_cost"]; ok {
		t.Fatal("nil resolver must be a no-op (no cost)")
	}
}

// TestEnrichTieredGraduated is R7 through the enrich stage: a long-context call against
// a tiered entry bills the above-threshold tranche at the tier rate (graduated), on the
// residual (§7.7).
func TestEnrichTieredGraduated(t *testing.T) {
	prices := &fakePrices{entries: map[string]*pricing.Entry{
		"google\x00gemini-1.5-pro": {
			ID: "google/gemini-1.5-pro#1", Provider: "google", Model: "gemini-1.5-pro", Version: 1,
			Rates: map[string]pricing.Rate{"input": {PerToken: 10}},
			Tiers: []pricing.Tier{{Key: "input", ThresholdTokens: 128000, PerToken: 20}},
		},
	}}
	out := run1(t, prices, map[string]any{
		"model": "gemini-1.5-pro", "provider": "google", "start_time": "2026-01-01T00:00:00Z",
		"provided_usage_details": map[string]any{"input": int64(200000)},
	})
	// 128000*10 + (200000-128000)*20 = 1,280,000 + 1,440,000 = 2,720,000.
	if out["total_cost"] != 2_720_000.0 {
		t.Fatalf("tiered total_cost=%v want 2720000 (graduated on residual)", out["total_cost"])
	}
}

// TestR5NoDoubleCountEndToEnd is the PRIORITY dual-incumbent proof, end-to-end through
// normalize+enrich: an agent aggregate span carrying usage + a leaf carrying the same
// usage. After normalize (drops the agent's usage, #81) and enrich, the AGENT span has
// NO cost and the LEAF is costed — so summing the trace's spans equals the leaf, not 2x.
func TestR5NoDoubleCountEndToEnd(t *testing.T) {
	prices := &fakePrices{entries: map[string]*pricing.Entry{
		"openai\x00gpt-4o": {ID: "openai/gpt-4o#1", Provider: "openai", Model: "gpt-4o", Version: 1,
			Rates: map[string]pricing.Rate{"input": {PerToken: 10}, "output": {PerToken: 20}}},
	}}
	s := &enrichStage{prices: prices}
	reg := normalize.Default()
	nctx := normalize.Context{ProjectID: "p"}
	mk := func(op, spanID string) map[string]any {
		return reg.Normalize(normalize.SpanInput{
			TraceID: "tr", SpanID: spanID, Name: op, Attributes: map[string]any{
				"gen_ai.operation.name": op, "gen_ai.request.model": "gpt-4o", "gen_ai.provider.name": "openai",
				"gen_ai.usage.input_tokens": int64(1000), "gen_ai.usage.output_tokens": int64(500),
				"gen_ai.response.completion_start_time": "2026-01-01T00:00:00Z",
			}}, nctx)
	}
	agent := mk("invoke_agent", "agg")
	leaf := mk("chat", "leaf")
	agent["start_time"] = "2026-01-01T00:00:00Z"
	leaf["start_time"] = "2026-01-01T00:00:00Z"
	ing := &Ingestion{Identity: controlplane.Identity{ProjectID: "p"}, Events: []storage.Event{
		{Payload: agent}, {Payload: leaf},
	}}
	if err := s.Process(context.Background(), ing); err != nil {
		t.Fatal(err)
	}
	if _, ok := agent["total_cost"]; ok {
		t.Fatalf("the aggregate agent span must have NO cost (would double-count), got %v", agent["total_cost"])
	}
	leafCost, _ := leaf["total_cost"].(float64)
	want := 1000*10.0 + 500*20.0 // 20000
	if leafCost != want {
		t.Fatalf("leaf total_cost=%v want %v", leafCost, want)
	}
	// The trace-level sum = leaf only (not 2x): agent contributes 0.
	traceSum := leafCost // agent contributes nothing
	if traceSum != want {
		t.Fatalf("trace-level cost must equal the leaf (%v), not 2x", want)
	}
}

// fakeDiscountErr errors on GetDiscount to exercise the discount fail-safe.
type fakeDiscountErr struct{ *fakePrices }

func (f *fakeDiscountErr) GetDiscount(context.Context, string) (float64, bool, error) {
	return 0, false, errors.New("discount db down")
}

// TestEnrichDiscountErrorLeavesNull is the M2-review fix: a discount-lookup error must
// leave cost NULL (backfillable), NOT derive at full price (which would silently
// overcharge a discounted project and be undetectable by a null-based re-pricing).
func TestEnrichDiscountErrorLeavesNull(t *testing.T) {
	base := &fakePrices{entries: map[string]*pricing.Entry{
		"openai\x00gpt-4o": {ID: "openai/gpt-4o#1", Provider: "openai", Model: "gpt-4o", Version: 1,
			Rates: map[string]pricing.Rate{"input": {PerToken: 10}}},
	}}
	out := run1(t, &fakeDiscountErr{base}, map[string]any{
		"model": "gpt-4o", "provider": "openai", "start_time": "2026-01-01T00:00:00Z",
		"provided_usage_details": map[string]any{"input": int64(100)},
	})
	if _, ok := out["total_cost"]; ok {
		t.Fatal("a discount-lookup error must leave cost NULL (not derive at full price)")
	}
	if out["cost_source"] != nil {
		t.Fatalf("cost_source must be unset on discount error, got %v", out["cost_source"])
	}
}
