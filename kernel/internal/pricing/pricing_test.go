package pricing

import (
	"math"
	"testing"
)

// TestCanonicalModelSymmetric is the R6 prove-the-negative (Opik #5621: a prefix
// stripped at load but not at lookup → all LiteLLM OTel spans record cost=0). The same
// normalizer is applied at load and lookup, so a prefixed name and its bare form MUST
// canonicalize to the SAME key — and canonicalizing an already-canonical key is a
// fixpoint (idempotent), which is what guarantees load==lookup regardless of which side
// saw the prefix.
func TestCanonicalModelSymmetric(t *testing.T) {
	groups := [][]string{
		{"gpt-4o", "openai/gpt-4o", "openrouter/openai/gpt-4o", "OpenAI/GPT-4o"},
		{"gpt-3.5-turbo", "openrouter/openai/gpt-3.5-turbo"},
		{"claude-3-5-sonnet-20241022", "anthropic/claude-3-5-sonnet-20241022"},
		{"gemini-1.5-pro", "vertex_ai/gemini-1.5-pro", "gemini/gemini-1.5-pro", "google/gemini-1.5-pro"},
	}
	for _, g := range groups {
		want := CanonicalModel(g[0])
		for _, raw := range g {
			if got := CanonicalModel(raw); got != want {
				t.Errorf("CanonicalModel(%q)=%q, want %q — asymmetric normalization = silent zero cost (R6)", raw, got, want)
			}
			// Idempotence: canonicalizing the canonical form is a fixpoint, so load and
			// lookup agree no matter how many times normalization is applied.
			if got := CanonicalModel(CanonicalModel(raw)); got != want {
				t.Errorf("CanonicalModel not idempotent for %q: %q", raw, got)
			}
		}
	}
}

// A model whose leading segment is NOT a known provider/router prefix is preserved
// verbatim (it is part of the model name, not a prefix to strip).
func TestCanonicalModelPreservesNonPrefix(t *testing.T) {
	cases := map[string]string{
		"llama-3.1-70b":      "llama-3.1-70b",
		"custom-org/my-tune": "custom-org/my-tune", // unknown leading segment kept
		"meta-llama/Llama-3": "llama-3",            // meta-llama IS a known prefix
	}
	for in, want := range cases {
		if got := CanonicalModel(in); got != want {
			t.Errorf("CanonicalModel(%q)=%q, want %q", in, got, want)
		}
	}
}

// TestCanonicalProviderSingleTable is R6's provider half (Opik #6928: Vertex AI routed
// to provider='gemini' broke pricing AND credentials). The Google family collapses to
// ONE canonical identity through the single table; azure stays distinct from openai
// (they price differently); unknown providers pass through.
func TestCanonicalProviderSingleTable(t *testing.T) {
	cases := map[string]string{
		"vertex_ai": "google", "vertexai": "google", "gemini": "google",
		"google_ai": "google", "Google": "google",
		"azure": "azure", "azure_openai": "azure",
		"openai": "openai", "anthropic": "anthropic", "Claude": "anthropic",
		"bedrock": "bedrock", "aws": "bedrock",
		"some-new-provider": "some-new-provider", // data-driven passthrough
	}
	for in, want := range cases {
		if got := CanonicalProvider(in); got != want {
			t.Errorf("CanonicalProvider(%q)=%q, want %q", in, got, want)
		}
	}
	// azure must NOT collapse into openai (distinct pricing).
	if CanonicalProvider("azure") == CanonicalProvider("openai") {
		t.Fatal("azure and openai must stay distinct providers")
	}
}

func TestEntryID(t *testing.T) {
	if got := EntryID("openai", "gpt-4o", 3); got != "openai/gpt-4o#3" {
		t.Fatalf("EntryID = %q", got)
	}
}

// TestValidateRates rejects money-integrity landmines: negative/NaN/Inf rates, a bad
// reduces base, and non-positive tier thresholds — so no bad price data can land.
func TestValidateRates(t *testing.T) {
	ok := Entry{Rates: map[string]Rate{"input": {PerToken: 0.001, Reduces: "input"}}, Tiers: []Tier{{Key: "input", ThresholdTokens: 200000, PerToken: 0.002}}}
	if err := ok.ValidateRates(); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
	bad := []Entry{
		{Rates: map[string]Rate{"input": {PerToken: -0.001}}},
		{Rates: map[string]Rate{"input": {PerToken: math.Inf(1)}}},
		{Rates: map[string]Rate{"input": {PerToken: math.NaN()}}},
		{Rates: map[string]Rate{"input": {PerToken: 0.001, Reduces: "sideways"}}},
		{Rates: map[string]Rate{"input": {PerToken: 0.001}}, Tiers: []Tier{{Key: "input", ThresholdTokens: 0, PerToken: 0.002}}},
		{Rates: map[string]Rate{"input": {PerToken: 0.001}}, Tiers: []Tier{{Key: "input", ThresholdTokens: 100, PerToken: -1}}},
	}
	for i, e := range bad {
		if err := e.ValidateRates(); err == nil {
			t.Errorf("bad entry %d must be rejected", i)
		}
	}
}
