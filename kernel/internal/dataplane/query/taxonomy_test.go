package query

import (
	"testing"
	"time"
)

func compileErr(t *testing.T, doc map[string]any) *CompileError {
	t.Helper()
	_, err := CompileSpans(doc, "proj-1", 30*24*time.Hour)
	if err == nil {
		t.Fatalf("expected an error, got nil")
	}
	ce, ok := err.(*CompileError)
	if !ok {
		t.Fatalf("expected *CompileError, got %T", err)
	}
	return ce
}

func assertCode(t *testing.T, ce *CompileError, code string, status int) {
	t.Helper()
	if ce.Code != code || ce.Status != status {
		t.Fatalf("want %s/%d, got %s/%d (%s)", code, status, ce.Code, ce.Status, ce.Msg)
	}
}

// Unknown top-level keys are rejected 400 schema_invalid (typos never no-op).
func TestUnknownTopLevelKey(t *testing.T) {
	doc := baseDoc()
	doc["filtres"] = []any{} // typo
	assertCode(t, compileErr(t, doc), "schema_invalid", 400)
}

// Unknown field in a condition -> 422 unknown_field.
func TestUnknownFieldIs422(t *testing.T) {
	doc := baseDoc(map[string]any{"field": "nope", "op": "eq", "value": "x"})
	assertCode(t, compileErr(t, doc), "unknown_field", 422)
}

// Operator not allowed for the field's class -> 422 operator_not_allowed.
func TestOperatorNotAllowed(t *testing.T) {
	doc := baseDoc(map[string]any{"field": "name", "op": "gt", "value": "x"})
	assertCode(t, compileErr(t, doc), "operator_not_allowed", 422)
}

// Nesting beyond an AND-of-ORs -> 400 schema_invalid.
func TestNestingDepthGuard(t *testing.T) {
	doc := baseDoc(map[string]any{
		"any": []any{
			map[string]any{"any": []any{
				map[string]any{"field": "name", "op": "eq", "value": "x"},
			}},
		},
	})
	assertCode(t, compileErr(t, doc), "schema_invalid", 400)
}

// timeRange window over the ceiling -> 422 ceiling_exceeded.
func TestWindowCeiling(t *testing.T) {
	_, err := CompileSpans(baseDoc(), "proj-1", time.Hour) // 24h window > 1h max
	ce, _ := err.(*CompileError)
	if ce == nil {
		t.Fatal("expected ceiling error")
	}
	assertCode(t, ce, "ceiling_exceeded", 422)
}

// in-list over MAX_IN_LIST -> 400 schema_invalid.
func TestInListCeiling(t *testing.T) {
	big := make([]any, maxInList+1)
	for i := range big {
		big[i] = "x"
	}
	doc := baseDoc(map[string]any{"field": "environment", "op": "in", "value": big})
	assertCode(t, compileErr(t, doc), "schema_invalid", 400)
}

// A cursor from a different query shape is rejected 400 schema_invalid.
func TestCursorBoundToQueryShape(t *testing.T) {
	// First query produces a cursor for its shape.
	c1 := compile(t, baseDoc(map[string]any{"field": "name", "op": "eq", "value": "a"}))
	cur := EncodeCursor(c1.Fingerprint, time.Now().UTC(), "span-1")

	// Reuse that cursor against a DIFFERENT filter shape -> rejected.
	doc2 := baseDoc(map[string]any{"field": "name", "op": "eq", "value": "b"})
	doc2["cursor"] = cur
	assertCode(t, compileErr(t, doc2), "schema_invalid", 400)

	// Same shape -> accepted.
	doc3 := baseDoc(map[string]any{"field": "name", "op": "eq", "value": "a"})
	doc3["cursor"] = cur
	if _, err := CompileSpans(doc3, "proj-1", 30*24*time.Hour); err != nil {
		t.Fatalf("same-shape cursor should be accepted: %v", err)
	}
}

// A malformed cursor is rejected 400 schema_invalid.
func TestMalformedCursor(t *testing.T) {
	doc := baseDoc()
	doc["cursor"] = "!!!not-base64!!!"
	assertCode(t, compileErr(t, doc), "schema_invalid", 400)
}
