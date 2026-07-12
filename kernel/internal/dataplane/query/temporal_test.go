package query

import (
	"strings"
	"testing"
	"time"
)

// duration/ttft compile to guarded epoch expressions and are orderable.
func TestComputedTemporalFields(t *testing.T) {
	c := compile(t, baseDoc(map[string]any{"field": "duration", "op": "gt", "value": 5}))
	if !strings.Contains(c.Where, "EXTRACT(EPOCH FROM (end_time - start_time)) >") {
		t.Fatalf("duration should compile to an epoch expression: %s", c.Where)
	}

	c = compile(t, baseDoc(map[string]any{"field": "ttft", "op": "lte", "value": 0.5}))
	if !strings.Contains(c.Where, "EXTRACT(EPOCH FROM (completion_start_time - start_time)) <=") {
		t.Fatalf("ttft should compile to an epoch expression: %s", c.Where)
	}

	doc := baseDoc()
	doc["orderBy"] = []any{map[string]any{"field": "duration", "dir": "desc"}}
	c = compile(t, doc)
	if !strings.Contains(c.Order, "EXTRACT(EPOCH FROM (end_time - start_time)) DESC") {
		t.Fatalf("order by duration should use the expression: %s", c.Order)
	}
}

// completion_start_time is queryable as a timestamp.
func TestCompletionStartTimeQueryable(t *testing.T) {
	_, err := CompileSpans(baseDoc(map[string]any{
		"field": "completion_start_time", "op": "gte", "value": "2026-01-01T00:00:00Z",
	}), "p", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("completion_start_time should be queryable: %v", err)
	}
}
