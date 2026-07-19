package normalize

import "testing"

// TestNullUsageDetailObjects is the regression for a bad-data-never-fails-a-valid-path
// hazard a second incumbent hit: an emitter sends a usage object whose nested detail
// objects are null (prompt_tokens_details=null, completion_tokens_details=null) and the
// usage builder crashes on the null instead of treating the buckets as absent.
//
// Our attributes arrive already flattened from OTLP (pdata AsRaw turns a null KVList into
// a nil value and an empty KVList into an empty map), so the hazard shows up here as
// detail keys whose value is nil, plus a nested detail object present-but-nil or empty.
// The rule: a null/empty detail object contributes NO buckets and never errors — the
// primary counts are still read, and cost derivation later sees only the real buckets.
func TestNullUsageDetailObjects(t *testing.T) {
	in := SpanInput{
		TraceID: "t", SpanID: "s", Name: "chat",
		Attributes: map[string]any{
			"gen_ai.operation.name":      "chat",
			"gen_ai.request.model":       "gpt-4o",
			"gen_ai.usage.input_tokens":  int64(100),
			"gen_ai.usage.output_tokens": int64(20),
			// Null detail LEAF keys (a null cached_tokens / audio_tokens).
			"gen_ai.usage.prompt_tokens_details.cached_tokens": nil,
			"gen_ai.usage.input_tokens_details.audio_tokens":   nil,
			// Null / empty nested detail OBJECTS (a null or {} KVList after AsRaw).
			"gen_ai.usage.prompt_tokens_details":     nil,
			"gen_ai.usage.completion_tokens_details": map[string]any{},
		},
	}

	// The whole map must complete without panicking on any null.
	out := (&SemConv{}).Map(in, Context{ProjectID: "p"})

	u, ok := out["provided_usage_details"].(map[string]any)
	if !ok {
		t.Fatalf("provided_usage_details not set from the valid primary counts: %v", out["provided_usage_details"])
	}
	// The real primary counts survive.
	if u["input"] != int64(100) || u["output"] != int64(20) {
		t.Fatalf("primary usage counts not read past the null details: %v", u)
	}
	// total is synthesized from input+output (no provider total sent).
	if u["total"] != int64(120) {
		t.Fatalf("total should synthesize to input+output=120, got %v", u["total"])
	}
	// A null detail leaf contributes NO bucket — never a silent zero.
	if _, present := u["cache_read"]; present {
		t.Fatalf("a null cached_tokens must not produce a cache_read bucket, got %v", u)
	}
	if _, present := u["audio_input"]; present {
		t.Fatalf("a null audio_tokens must not produce an audio_input bucket, got %v", u)
	}
}

// TestAllNullUsageIsNoUsage proves the degenerate case: a usage object made up ENTIRELY
// of null detail keys yields no usage map at all (not an empty-but-present one), so a
// downstream model-without-usage span produces no fabricated cost.
func TestAllNullUsageIsNoUsage(t *testing.T) {
	in := SpanInput{
		TraceID: "t", SpanID: "s", Name: "chat",
		Attributes: map[string]any{
			"gen_ai.operation.name":                            "chat",
			"gen_ai.request.model":                             "gpt-4o",
			"gen_ai.usage.input_tokens":                        nil,
			"gen_ai.usage.output_tokens":                       nil,
			"gen_ai.usage.prompt_tokens_details.cached_tokens": nil,
		},
	}
	out := (&SemConv{}).Map(in, Context{ProjectID: "p"})
	if u, ok := out["provided_usage_details"]; ok {
		t.Fatalf("all-null usage must yield NO usage map (model-without-usage), got %v", u)
	}
	// The model is still promoted (informational); with no usage, derivation fabricates
	// no cost — the no-cost-without-usage rule holds by construction.
	if out["model"] != "gpt-4o" {
		t.Fatalf("model should still be promoted, got %v", out["model"])
	}
	if _, ok := out["cost_source"]; ok {
		t.Fatalf("no cost_source must be stamped when there is no usage, got %v", out["cost_source"])
	}
}
