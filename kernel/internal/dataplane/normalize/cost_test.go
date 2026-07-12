package normalize

import (
	"math"
	"testing"
)

// Provided-cost passthrough regression: when the client
// sends cost, it must flow to provided_cost_details, stamp cost_source=provided,
// and populate the promoted total_cost — even without kernel derivation.
func TestProvidedCostPassthrough(t *testing.T) {
	in := SpanInput{
		TraceID: "t", SpanID: "s", Name: "chat",
		Attributes: map[string]any{
			"gen_ai.operation.name":     "chat",
			"gen_ai.request.model":      "llama-3-70b",
			"gen_ai.usage.input_cost":   0.0012,
			"gen_ai.usage.output_cost":  0.0008,
			"gen_ai.usage.input_tokens": int64(100),
		},
	}
	out := (&SemConv{}).Map(in, Context{ProjectID: "p"})

	pcd, ok := out["provided_cost_details"].(map[string]any)
	if !ok {
		t.Fatalf("provided_cost_details not set: %v", out["provided_cost_details"])
	}
	if pcd["input"] != 0.0012 || pcd["output"] != 0.0008 {
		t.Fatalf("provided cost not preserved: %v", pcd)
	}
	if total, _ := pcd["total"].(float64); total < 0.0019 || total > 0.0021 {
		t.Fatalf("total should sum input+output, got %v", pcd["total"])
	}
	if out["cost_source"] != "provided" {
		t.Fatalf("cost_source should be 'provided', got %v", out["cost_source"])
	}
	if tc, _ := out["total_cost"].(float64); tc < 0.0019 || tc > 0.0021 {
		t.Fatalf("total_cost should be populated from provided total, got %v", out["total_cost"])
	}
}

// An explicit total cost wins over the summed input+output.
func TestProvidedCostExplicitTotal(t *testing.T) {
	in := SpanInput{
		TraceID: "t", SpanID: "s", Name: "chat",
		Attributes: map[string]any{
			"gen_ai.operation.name": "chat",
			"gen_ai.request.model":  "gpu-local",
			"gen_ai.usage.cost":     0.5, // GPU-seconds cost
		},
	}
	out := (&SemConv{}).Map(in, Context{ProjectID: "p"})
	if out["total_cost"] != 0.5 {
		t.Fatalf("explicit total cost should win, got %v", out["total_cost"])
	}
}

// TestAggregateSpanUsageDropped is the no-double-count prove-the-negative: an
// aggregate agent span carrying usage that duplicates
// its child must NOT have that usage extracted (or trace-level cost double-counts).
func TestAggregateSpanUsageDropped(t *testing.T) {
	agg := SpanInput{TraceID: "t", SpanID: "agg", Name: "invoke_agent x", Attributes: map[string]any{
		"gen_ai.operation.name": "invoke_agent", "gen_ai.request.model": "gpt-4o",
		"gen_ai.usage.input_tokens": int64(812), "gen_ai.usage.output_tokens": int64(96),
		"gen_ai.usage.input_cost": 0.5,
	}}
	out := (&SemConv{}).Map(agg, Context{ProjectID: "p"})
	if _, ok := out["provided_usage_details"]; ok {
		t.Fatal("an aggregate agent span must NOT carry usage (double-count with its child)")
	}
	if _, ok := out["provided_cost_details"]; ok {
		t.Fatal("an aggregate agent span must NOT carry provided cost")
	}
	// The model is still promoted (informational) — only usage/cost are withheld.
	if out["model"] != "gpt-4o" {
		t.Fatalf("model should still be promoted, got %v", out["model"])
	}
	// A real model call (chat) with the SAME usage keeps it.
	leaf := SpanInput{TraceID: "t", SpanID: "leaf", Name: "chat", Attributes: map[string]any{
		"gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-4o",
		"gen_ai.usage.input_tokens": int64(812), "gen_ai.usage.output_tokens": int64(96),
	}}
	lout := (&SemConv{}).Map(leaf, Context{ProjectID: "p"})
	if lout["provided_usage_details"] == nil {
		t.Fatal("a model-call leaf must keep its usage")
	}
}

// TestAudioBucketsMapped proves audio tokens map to dedicated buckets so cost
// derivation can price them at the audio rate, not the text rate.
func TestAudioBucketsMapped(t *testing.T) {
	in := SpanInput{TraceID: "t", SpanID: "s", Name: "chat", Attributes: map[string]any{
		"gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-4o-audio-preview",
		"gen_ai.usage.input_tokens": int64(500), "gen_ai.usage.output_tokens": int64(200),
		"gen_ai.usage.input_audio_tokens": int64(300), "gen_ai.usage.output_audio_tokens": int64(150),
	}}
	out := (&SemConv{}).Map(in, Context{ProjectID: "p"})
	u, _ := out["provided_usage_details"].(map[string]any)
	if u["audio_input"] != int64(300) || u["audio_output"] != int64(150) {
		t.Fatalf("audio tokens must map to audio_input/audio_output, got %v", u)
	}
	if u["input"] != int64(500) || u["output"] != int64(200) {
		t.Fatalf("text tokens must remain distinct from audio, got %v", u)
	}
}

// TestProvidedCostRejectsBadValues is the money-integrity fix: a NaN/Inf/
// negative provided cost is DROPPED (not stored verbatim), so one crafted span can't
// poison SUM(total_cost) across the project.
func TestProvidedCostRejectsBadValues(t *testing.T) {
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.5} {
		in := SpanInput{TraceID: "t", SpanID: "s", Name: "chat", Attributes: map[string]any{
			"gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-4o",
			"gen_ai.usage.cost": bad,
		}}
		out := (&SemConv{}).Map(in, Context{ProjectID: "p"})
		if pcd, ok := out["provided_cost_details"].(map[string]any); ok {
			if _, has := pcd["total"]; has {
				t.Fatalf("bad provided cost %v must be dropped, got %v", bad, pcd)
			}
		}
		if _, ok := out["total_cost"]; ok {
			t.Fatalf("bad provided cost %v must not reach total_cost", bad)
		}
	}
	// A good value still flows.
	good := (&SemConv{}).Map(SpanInput{TraceID: "t", SpanID: "s", Name: "chat", Attributes: map[string]any{
		"gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-4o", "gen_ai.usage.cost": 0.5,
	}}, Context{ProjectID: "p"})
	if good["total_cost"] != 0.5 {
		t.Fatalf("a valid provided cost must flow, got %v", good["total_cost"])
	}
}

// TestAggregateGateCaseFold: the aggregate-span gate is case-insensitive (defense-in-depth) — an
// oddly-cased aggregate op still drops usage.
func TestAggregateGateCaseFold(t *testing.T) {
	in := SpanInput{TraceID: "t", SpanID: "s", Name: "agent", Attributes: map[string]any{
		"gen_ai.operation.name": "Invoke_Agent", "gen_ai.request.model": "gpt-4o",
		"gen_ai.usage.input_tokens": int64(100),
	}}
	out := (&SemConv{}).Map(in, Context{ProjectID: "p"})
	if _, ok := out["provided_usage_details"]; ok {
		t.Fatal("a case-variant aggregate op must still drop usage")
	}
}
