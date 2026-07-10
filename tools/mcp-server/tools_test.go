package main

import "testing"

func TestToolDefs(t *testing.T) {
	defs := toolDefs()
	if len(defs) != 5 {
		t.Fatalf("expected 5 tools, got %d", len(defs))
	}
	for _, d := range defs {
		if d.Name == "" || d.Description == "" || d.InputSchema == nil {
			t.Fatalf("tool %q missing name/description/schema", d.Name)
		}
		if d.InputSchema["type"] != "object" {
			t.Fatalf("tool %q inputSchema must be an object", d.Name)
		}
	}
}

func TestSummarizeSpanIsMetadataOnly(t *testing.T) {
	sp := map[string]any{
		"id": "s1", "trace_id": "t", "kind": "generation", "name": "chat",
		"start_time": "2026-01-01T00:00:00Z", "end_time": "2026-01-01T00:00:02Z",
		"status": map[string]any{"code": "ok"}, "model": "gpt-4o",
		"usage_details": map[string]any{"input": 10.0},
		// these must never appear in a summary even if present:
		"input": "SECRET", "output": "SECRET", "attributes": map[string]any{"x": "SECRET"},
	}
	out := summarizeSpan(sp)
	for _, f := range []string{"input", "output", "attributes", "events"} {
		if _, ok := out[f]; ok {
			t.Fatalf("summary leaked %q", f)
		}
	}
	if out["status"] != "ok" {
		t.Fatalf("status not flattened: %v", out["status"])
	}
	if out["duration_ms"] != int64(2000) {
		t.Fatalf("duration_ms wrong: %v", out["duration_ms"])
	}
	if out["model"] != "gpt-4o" {
		t.Fatalf("model missing")
	}
}

func TestClampLimit(t *testing.T) {
	if clampLimit(map[string]any{}) != defaultLimit {
		t.Fatal("default limit")
	}
	if clampLimit(map[string]any{"limit": float64(1000)}) != maxLimit {
		t.Fatal("max limit cap")
	}
	if clampLimit(map[string]any{"limit": float64(5)}) != 5 {
		t.Fatal("explicit limit")
	}
}
