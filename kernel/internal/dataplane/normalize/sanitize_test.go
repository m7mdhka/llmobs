package normalize

import "testing"

func TestSanitizeAttributeKeys(t *testing.T) {
	attrs := map[string]any{
		"gen_ai.request.model": "gpt-4o", // dotted, legal — untouched
		"bad\x01key":           "v1",     // SOH in the middle
		"tail\x7f":             "v2",     // DEL at the end
		"ok":                   "v3",
	}
	SanitizeAttributeKeys(attrs)

	if _, ok := attrs["gen_ai.request.model"]; !ok {
		t.Fatal("dotted legal key must be preserved untouched")
	}
	if _, ok := attrs["bad\x01key"]; ok {
		t.Fatal("control-char key must be removed")
	}
	if v, _ := attrs["badkey"].(string); v != "v1" {
		t.Fatalf("sanitized key should carry the value, got %v", attrs["badkey"])
	}
	if raw, _ := attrs["llmobs.raw.attr_key.badkey"].(string); raw != "bad\x01key" {
		t.Fatalf("original key must be preserved under llmobs.raw.attr_key.*, got %v", raw)
	}
	if _, ok := attrs["tail"]; !ok {
		t.Fatal("DEL-suffixed key must be sanitized to 'tail'")
	}
	if n, _ := attrs["llmobs.dq.sanitized_attribute_keys"].(int); n != 2 {
		t.Fatalf("dq counter should be 2, got %v", attrs["llmobs.dq.sanitized_attribute_keys"])
	}
}

func TestSanitizeAttributeKeysNoop(t *testing.T) {
	attrs := map[string]any{"a.b.c": 1, "plain": 2}
	SanitizeAttributeKeys(attrs)
	if len(attrs) != 2 {
		t.Fatalf("clean keys must not add dq/raw entries, got %d keys", len(attrs))
	}
}
