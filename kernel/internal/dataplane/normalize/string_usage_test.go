package normalize

import "testing"

// TestStringTypedUsageCounts is the #130 regression: some exporters emit usage counts as an
// OTLP StringValue ("812") rather than an IntValue. They must be parsed, not dropped —
// dropping a bucket silently under-counts tokens AND cost. A non-numeric string is treated as
// absent (never a silent zero).
func TestStringTypedUsageCounts(t *testing.T) {
	in := SpanInput{
		TraceID: "t", SpanID: "s", Name: "chat",
		Attributes: map[string]any{
			"gen_ai.operation.name":      "chat",
			"gen_ai.request.model":       "gpt-4o",
			"gen_ai.usage.input_tokens":  "812",   // string-typed
			"gen_ai.usage.output_tokens": "96",    // string-typed
			"gen_ai.usage.total_tokens":  "908.0", // float-shaped string
		},
	}
	out := (&SemConv{}).Map(in, Context{ProjectID: "p"})
	u, ok := out["provided_usage_details"].(map[string]any)
	if !ok {
		t.Fatalf("usage_details not set from string-typed counts: %v", out["provided_usage_details"])
	}
	if u["input"] != int64(812) || u["output"] != int64(96) || u["total"] != int64(908) {
		t.Fatalf("string usage not parsed correctly: %v", u)
	}

	// A non-numeric string is NOT a count — the bucket is absent, not a silent zero.
	in2 := SpanInput{
		TraceID: "t", SpanID: "s", Name: "chat",
		Attributes: map[string]any{
			"gen_ai.operation.name":     "chat",
			"gen_ai.request.model":      "gpt-4o",
			"gen_ai.usage.input_tokens": "n/a",
		},
	}
	out2 := (&SemConv{}).Map(in2, Context{ProjectID: "p"})
	if u2, ok := out2["provided_usage_details"].(map[string]any); ok {
		if _, present := u2["input"]; present {
			t.Fatalf("a non-numeric string must not produce a bucket, got %v", u2)
		}
	}
}
