package query

import (
	"strings"
	"testing"
	"time"
)

// baseDoc is a minimal valid spans query; tests append a filter.
func baseDoc(filters ...any) map[string]any {
	return map[string]any{
		"target": "spans",
		"timeRange": map[string]any{
			"from": "2026-01-01T00:00:00Z",
			"to":   "2026-01-02T00:00:00Z",
		},
		"filters": filters,
	}
}

func compile(t *testing.T, doc map[string]any) *Compiled {
	t.Helper()
	c, err := CompileSpans(doc, "proj-1", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return c
}

// Decision 2: not_in negations must match unset (NULL) rows.
func TestNotInMatchesNull(t *testing.T) {
	c := compile(t, baseDoc(map[string]any{
		"field": "environment", "op": "not_in", "value": []any{"prod", "staging"},
	}))
	if !strings.Contains(c.Where, "environment IS NULL OR environment <> ALL(") {
		t.Fatalf("not_in should match NULL rows, got: %s", c.Where)
	}
	if strings.Contains(c.Where, "NOT (") {
		t.Fatalf("not_in should not compile to NOT(=ANY), got: %s", c.Where)
	}
}

// Decision 3: numeric map casts are guarded so non-numeric/missing values never
// raise a cast error — they simply don't match (CASE guarantees the cast is
// only evaluated on JSON numbers, not a bare guard AND cast).
func TestNumericMapCastIsGuarded(t *testing.T) {
	for _, op := range []string{"eq", "gt", "gte", "lt", "lte"} {
		c := compile(t, baseDoc(map[string]any{
			"field": "usage_details", "key": "input_tokens", "op": op, "value": 100,
		}))
		if !strings.Contains(c.Where, "jsonb_typeof(") || !strings.Contains(c.Where, "= 'number' THEN") {
			t.Fatalf("op %s: cast not guarded by jsonb_typeof: %s", op, c.Where)
		}
		if !strings.Contains(c.Where, "ELSE false END") {
			t.Fatalf("op %s: positive numeric op should not match unset, got: %s", op, c.Where)
		}
	}
}

// Decision 3 + Decision 2: numeric map neq is a negation — unset/non-number rows match.
func TestNumericMapNeqMatchesUnset(t *testing.T) {
	c := compile(t, baseDoc(map[string]any{
		"field": "usage_details", "key": "input_tokens", "op": "neq", "value": 100,
	}))
	if !strings.Contains(c.Where, "= 'number' THEN") || !strings.Contains(c.Where, "ELSE true END") {
		t.Fatalf("numeric map neq should match unset via ELSE true, got: %s", c.Where)
	}
	if !strings.Contains(c.Where, "IS DISTINCT FROM") {
		t.Fatalf("numeric map neq should use IS DISTINCT FROM, got: %s", c.Where)
	}
}

// A wrong-typed value on a valid query is not a 422: it compiles and simply
// won't match (DSL §9.1 — bad data never fails a valid query).
func TestWrongTypedNumericValueCompiles(t *testing.T) {
	_, err := CompileSpans(baseDoc(map[string]any{
		"field": "usage_details", "key": "input_tokens", "op": "gt", "value": "not-a-number",
	}), "proj-1", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("wrong-typed value should compile, got error: %v", err)
	}
}
