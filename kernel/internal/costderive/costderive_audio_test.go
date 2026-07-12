package costderive

import (
	"context"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/pricing"
)

type fakePrices struct{ e *pricing.Entry }

func (f fakePrices) Resolve(context.Context, string, string, time.Time) (*pricing.Entry, error) {
	return f.e, nil
}

func approxf(a, b float64) bool { d := a - b; return d < 1e-12 && d > -1e-12 }

// TestDeriveSpanCostAudioPricedAtAudioRateNotText is the #79 END-TO-END prove-the-negative
// (the Opik #7137 bug): an audio-model span with audio tokens in dedicated buckets must derive
// cost that prices audio at its OWN (often ~16×) rate and bills the TEXT base only on the
// residual — never folding audio into the text buckets at the text rate (a massive
// under-charge). Exercises the shared DeriveSpanCost that both ingest-enrich and re-pricing use.
func TestDeriveSpanCostAudioPricedAtAudioRateNotText(t *testing.T) {
	// A real audio span, post-normalize (audio in its own buckets; the mapping is proven by the
	// otel-genai audio fixtures + TestAudioBucketsMapped).
	p := map[string]any{
		"model": "gpt-4o-audio-preview", "provider": "openai",
		"provided_usage_details": map[string]any{
			"input": int64(500), "output": int64(200),
			"audio_input": int64(300), "audio_output": int64(150),
		},
	}
	// Data-driven price entry (§7.7): text rates + SEPARATE audio rates that REDUCE the text
	// base. Audio input at 16× the text rate — the exact premium Opik #7137 lost by billing
	// audio as text.
	entry := &pricing.Entry{
		Provider: "openai", Model: "gpt-4o-audio-preview", Version: 1, ID: "openai/gpt-4o-audio-preview#1",
		Rates: map[string]pricing.Rate{
			"input":        {PerToken: 0.0000025},
			"output":       {PerToken: 0.00001},
			"audio_input":  {PerToken: 0.00004, Reduces: "input"},
			"audio_output": {PerToken: 0.00008, Reduces: "output"},
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
	// Audio priced at the AUDIO rate, in its own bucket.
	if !approxf(cd["audio_input"].(float64), 300*0.00004) { // 0.012
		t.Fatalf("audio_input must bill at the audio rate 0.012, got %v", cd["audio_input"])
	}
	if !approxf(cd["audio_output"].(float64), 150*0.00008) { // 0.012
		t.Fatalf("audio_output must bill at the audio rate 0.012, got %v", cd["audio_output"])
	}
	// Text base billed on the RESIDUAL only (input−audio_input, output−audio_output).
	if !approxf(cd["input"].(float64), 200*0.0000025) { // (500-300)*rate = 0.0005
		t.Fatalf("input must bill on the residual 200 tokens, got %v", cd["input"])
	}
	if !approxf(cd["output"].(float64), 50*0.00001) { // (200-150)*rate = 0.0005
		t.Fatalf("output must bill on the residual 50 tokens, got %v", cd["output"])
	}
	total := p["total_cost"].(float64)
	const correct = 0.012 + 0.012 + 0.0005 + 0.0005 // 0.025
	if !approxf(total, correct) {
		t.Fatalf("total_cost = %v, want %v", total, correct)
	}
	// PROVE-THE-NEGATIVE: if audio had been folded into text at the TEXT rate (the incumbent
	// bug), the total would be input*txt + output*txt = 500*0.0000025 + 200*0.00001 = 0.00325 —
	// ~7.7× lower. A correct derivation must NOT produce that.
	const asTextBug = 500*0.0000025 + 200*0.00001 // 0.00325
	if approxf(total, asTextBug) {
		t.Fatalf("audio was billed at the TEXT rate (Opik #7137 undercharge): total=%v", total)
	}
	if got.ID != entry.ID || p["cost_source"] != "derived" {
		t.Fatalf("cost provenance wrong: source=%v ref=%v", p["cost_source"], got.ID)
	}
}

// TestDeriveSpanCostAudioNoRateNotBilled is the data-driven negative (R2/R4): if the price
// entry declares NO audio rate, audio tokens are simply not billed — never invented and never
// folded into text. The base residual is still reduced by the (unpriced) audio tokens? No —
// a bucket only reduces its base if that bucket's rate declares Reduces. An unpriced audio
// bucket neither bills nor reduces, so the text base is billed on its full count.
func TestDeriveSpanCostAudioNoRateNotBilled(t *testing.T) {
	p := map[string]any{
		"model": "some-audio-model", "provider": "acme",
		"provided_usage_details": map[string]any{
			"input": int64(500), "audio_input": int64(300),
		},
	}
	entry := &pricing.Entry{
		Provider: "acme", Model: "some-audio-model", Version: 1, ID: "acme/some-audio-model#1",
		Rates:    map[string]pricing.Rate{"input": {PerToken: 0.0000025}}, // no audio rate
	}
	if _, err := DeriveSpanCost(context.Background(), p, fakePrices{entry}, 1.0, map[string]*pricing.Entry{}); err != nil {
		t.Fatal(err)
	}
	cd := p["cost_details"].(map[string]any)
	if _, billed := cd["audio_input"]; billed {
		t.Fatalf("an unpriced audio bucket must NOT be billed (data-driven), got %v", cd["audio_input"])
	}
	// No audio rate → no Reduces → input billed on its full 500 (not reduced by audio).
	if !approxf(cd["input"].(float64), 500*0.0000025) {
		t.Fatalf("input must bill full 500 when audio is unpriced, got %v", cd["input"])
	}
}
