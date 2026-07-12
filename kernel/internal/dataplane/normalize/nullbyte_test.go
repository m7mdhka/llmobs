package normalize

import "testing"

// TestSanitizeNullBytes proves NUL (U+0000) is stripped from every string value —
// attribute values, nested maps/slices, and promoted fields (input/output) — so the span never
// fails the lite Postgres text/JSONB INSERT (22P05) while landing on scale. Other control chars
// (tab/newline) are legitimate and left intact. A dq counter records the change.
func TestSanitizeNullBytes(t *testing.T) {
	out := map[string]any{
		"name":   "chat\x00with-nul",
		"input":  "prompt\x00body",        // promoted opaque payload
		"output": "ok\ttab-and-newline\n", // legitimate control chars, must survive
		"attributes": map[string]any{
			"k":      "val\x00ue",
			"nested": map[string]any{"deep": "a\x00b"},
			"list":   []any{"x\x00y", "clean"},
			"num":    int64(5),
		},
	}
	SanitizeNullBytes(out)

	if out["name"] != "chatwith-nul" {
		t.Fatalf("name NUL not stripped: %q", out["name"])
	}
	if out["input"] != "promptbody" {
		t.Fatalf("promoted input NUL not stripped: %q", out["input"])
	}
	if out["output"] != "ok\ttab-and-newline\n" {
		t.Fatalf("legitimate control chars must survive, got %q", out["output"])
	}
	a := out["attributes"].(map[string]any)
	if a["k"] != "value" {
		t.Fatalf("attr value NUL not stripped: %q", a["k"])
	}
	if a["nested"].(map[string]any)["deep"] != "ab" {
		t.Fatalf("nested NUL not stripped: %v", a["nested"])
	}
	if lst := a["list"].([]any); lst[0] != "xy" || lst[1] != "clean" {
		t.Fatalf("list NUL not stripped: %v", lst)
	}
	// dq counter: 5 strings carried a NUL (name, input, k, deep, list[0]).
	if n, _ := out["llmobs.dq.sanitized_null_bytes"].(int); n != 5 {
		t.Fatalf("dq sanitized_null_bytes = %v, want 5", out["llmobs.dq.sanitized_null_bytes"])
	}
	// No-op when clean: no dq key added.
	clean := map[string]any{"name": "fine", "attributes": map[string]any{"k": "v"}}
	SanitizeNullBytes(clean)
	if _, present := clean["llmobs.dq.sanitized_null_bytes"]; present {
		t.Fatal("clean span must not get a null-byte dq signal")
	}
}
