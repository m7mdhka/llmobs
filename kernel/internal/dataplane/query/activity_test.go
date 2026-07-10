package query

import (
	"strings"
	"testing"
	"time"
)

// is_open (boolean) and last_activity (timestamp) are queryable/orderable on
// traces — "active runs regardless of age" is a bounded query (Story 21).
func TestTraceActivityFields(t *testing.T) {
	c := compileTr(t, traceDoc(map[string]any{"field": "is_open", "op": "eq", "value": true}))
	if !strings.Contains(c.Where, "is_open = ") {
		t.Fatalf("is_open should compile to a boolean predicate: %s", c.Where)
	}

	c = compileTr(t, traceDoc(map[string]any{"field": "incomplete_trace", "op": "eq", "value": true}))
	if !strings.Contains(c.Where, "incomplete_trace = ") {
		t.Fatalf("incomplete_trace filterable: %s", c.Where)
	}

	doc := traceDoc()
	doc["orderBy"] = []any{map[string]any{"field": "last_activity", "dir": "desc"}}
	c = compileTr(t, doc)
	if !strings.Contains(c.Order, "last_activity DESC") {
		t.Fatalf("order by last_activity: %s", c.Order)
	}
}

// A boolean field rejects a non-boolean value (no coercion).
func TestBooleanFieldTypeCheck(t *testing.T) {
	_, err := CompileTraces(traceDoc(map[string]any{"field": "is_open", "op": "eq", "value": "yes"}), "p", 30*24*time.Hour)
	ce, _ := err.(*CompileError)
	if ce == nil || ce.Code != "operator_not_allowed" {
		t.Fatalf("string value on boolean field should be rejected, got %v", err)
	}
}
