package normalize

import "testing"

// LM-4 provided-cost passthrough (Dmitri's regression test): when the client
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
