package costderive

import (
	"context"
	"testing"

	"github.com/m7mdhka/llmobs/kernel/internal/pricing"
)

// TestDeriveSpanCostCacheReadPricedAtCacheRateNotInput is the #146 END-TO-END prove-the-negative
// (same class as #79): a prompt-cached OpenAI span reports its cached tokens under the NESTED
// spelling prompt_tokens_details.cached_tokens (Responses API: input_tokens_details.cached_tokens),
// which the normalizer now maps to the cache_read bucket. Cost derivation must price those cached
// tokens at the CHEAPER cache rate and bill the input base only on the residual — never leaving
// cached tokens folded into `input` at the full input text rate (a silent OVERCHARGE the customer
// pays). Exercises the shared DeriveSpanCost that both ingest-enrich and re-pricing use.
func TestDeriveSpanCostCacheReadPricedAtCacheRateNotInput(t *testing.T) {
	// A real cached span, post-normalize (cached tokens in the cache_read bucket; the nested-spelling
	// mapping is proven by the otel-genai cache-read-nested fixture).
	p := map[string]any{
		"model": "gpt-4o", "provider": "openai",
		"provided_usage_details": map[string]any{
			"input": int64(1000), "output": int64(200),
			"cache_read": int64(800),
		},
	}
	// Data-driven price entry (§7.7): input/output text rates + a SEPARATE cache_read rate that
	// REDUCES the input base. OpenAI prices cached input at ~0.25× the input rate (a DISCOUNT).
	entry := &pricing.Entry{
		Provider: "openai", Model: "gpt-4o", Version: 1, ID: "openai/gpt-4o#1",
		Rates: map[string]pricing.Rate{
			"input":      {PerToken: 0.0000025},
			"output":     {PerToken: 0.00001},
			"cache_read": {PerToken: 0.000000625, Reduces: "input"}, // 0.25× input
		},
	}

	got, err := DeriveSpanCost(context.Background(), p, fakePrices{entry}, 1.0, map[string]*pricing.Entry{})
	if err != nil || got == nil {
		t.Fatalf("derive: entry=%v err=%v", got, err)
	}
	cd, _ := p["cost_details"].(map[string]any)
	if cd == nil {
		t.Fatalf("no cost_details derived: %v", p["cost_details"])
	}
	// Cached tokens priced at the CACHE rate, in their own bucket.
	if !approxf(cd["cache_read"].(float64), 800*0.000000625) { // 0.0005
		t.Fatalf("cache_read must bill at the cache rate 0.0005, got %v", cd["cache_read"])
	}
	// Input base billed on the RESIDUAL only (input − cache_read = 200 tokens).
	if !approxf(cd["input"].(float64), 200*0.0000025) { // (1000-800)*rate = 0.0005
		t.Fatalf("input must bill on the residual 200 tokens, got %v", cd["input"])
	}
	if !approxf(cd["output"].(float64), 200*0.00001) { // 0.002
		t.Fatalf("output unchanged, got %v", cd["output"])
	}
	total := p["total_cost"].(float64)
	const correct = 0.0005 + 0.0005 + 0.002 // 0.003
	if !approxf(total, correct) {
		t.Fatalf("total_cost = %v, want %v", total, correct)
	}
	// PROVE-THE-NEGATIVE: if the cached tokens had stayed folded into `input` at the full input
	// text rate (the bug when the nested spelling is unmapped), the total would be
	// input*txt + output*txt = 1000*0.0000025 + 200*0.00001 = 0.0045 — the customer OVERPAYS by
	// 0.0015 (50% more) on the input side. A correct derivation must NOT produce that.
	const asInputBug = 1000*0.0000025 + 200*0.00001 // 0.0045
	if approxf(total, asInputBug) {
		t.Fatalf("cached tokens were billed at the full INPUT rate (overcharge): total=%v", total)
	}
	if got.ID != entry.ID || p["cost_source"] != "derived" {
		t.Fatalf("cost provenance wrong: source=%v ref=%v", p["cost_source"], got.ID)
	}
}
