package query

import (
	"strings"
	"testing"
	"time"
)

func traceDoc(filters ...any) map[string]any {
	return map[string]any{
		"target": "traces",
		"timeRange": map[string]any{
			"from": "2026-01-01T00:00:00Z",
			"to":   "2026-01-02T00:00:00Z",
		},
		"filters": filters,
	}
}

func compileTr(t *testing.T, doc map[string]any) *Compiled {
	t.Helper()
	c, err := CompileTraces(doc, "proj-1", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("compile traces: %v", err)
	}
	return c
}

// Trace fields compile against the synthesized projection columns.
func TestTracesCompilesPromotedFields(t *testing.T) {
	c := compileTr(t, traceDoc(
		map[string]any{"field": "environment", "op": "eq", "value": "prod"},
		map[string]any{"field": "session_id", "op": "eq", "value": "s1"},
	))
	if !strings.Contains(c.Where, "environment =") || !strings.Contains(c.Where, "session_id =") {
		t.Fatalf("trace filters did not compile: %s", c.Where)
	}
	if !strings.Contains(c.Where, "start_time >=") {
		t.Fatalf("mandatory timeRange missing: %s", c.Where)
	}
}

// A span-only field (kind) is not a trace field -> unknown_field 422.
func TestTracesRejectsSpanOnlyField(t *testing.T) {
	_, err := CompileTraces(traceDoc(map[string]any{"field": "kind", "op": "eq", "value": "generation"}), "p", 30*24*time.Hour)
	ce, _ := err.(*CompileError)
	if ce == nil || ce.Code != "unknown_field" || ce.Status != 422 {
		t.Fatalf("kind should be unknown on traces, got %v", err)
	}
}

// Ordering by a trace-orderable field works; a non-orderable one is rejected.
func TestTracesOrderable(t *testing.T) {
	doc := traceDoc()
	doc["orderBy"] = []any{map[string]any{"field": "start_time", "dir": "desc"}}
	compileTr(t, doc)

	doc["orderBy"] = []any{map[string]any{"field": "environment", "dir": "asc"}}
	_, err := CompileTraces(doc, "p", 30*24*time.Hour)
	ce, _ := err.(*CompileError)
	if ce == nil || ce.Code != "not_orderable" {
		t.Fatalf("environment is not orderable on traces, got %v", err)
	}
}

// The score semi-join (QD-9) compiles to an EXISTS over the scores table.
func TestTracesScoresSemiJoin(t *testing.T) {
	doc := traceDoc()
	doc["scores"] = []any{map[string]any{"name": "hallucination", "data_type": "numeric", "op": "lt", "value": 0.5}}
	c := compileTr(t, doc)
	if !strings.Contains(c.Where, "EXISTS (SELECT 1 FROM scores sc") {
		t.Fatalf("semi-join did not compile to EXISTS: %s", c.Where)
	}
	if !strings.Contains(c.Where, "sc.subject_id = trace_proj.id") || !strings.Contains(c.Where, "sc.value_numeric <") {
		t.Fatalf("semi-join predicate wrong: %s", c.Where)
	}
}

// data_type drives validation: a wrong-typed value is score_type_mismatch (422).
func TestScoreConditionTypeMismatch(t *testing.T) {
	doc := traceDoc()
	doc["scores"] = []any{map[string]any{"name": "h", "data_type": "numeric", "op": "lt", "value": "0.5"}}
	_, err := CompileTraces(doc, "p", 30*24*time.Hour)
	ce, _ := err.(*CompileError)
	if ce == nil || ce.Code != "score_type_mismatch" || ce.Status != 422 {
		t.Fatalf("string value on numeric score should be score_type_mismatch, got %v", err)
	}
}

// The scores DSL target compiles over the scores projection (timestamp anchor).
func TestCompileScoresTarget(t *testing.T) {
	doc := map[string]any{
		"target":    "scores",
		"timeRange": map[string]any{"from": "2026-01-01T00:00:00Z", "to": "2026-01-02T00:00:00Z"},
		"filters":   []any{map[string]any{"field": "name", "op": "eq", "value": "hallucination"}},
	}
	c, err := CompileScores(doc, "p", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("scores target compile: %v", err)
	}
	if !strings.Contains(c.Where, "timestamp >=") {
		t.Fatalf("scores time anchor should be timestamp: %s", c.Where)
	}
}

// Spans still reject a scores block (scores are traces-only) with 400.
func TestSpansRejectScores(t *testing.T) {
	doc := baseDoc()
	doc["scores"] = []any{map[string]any{"name": "x", "data_type": "numeric"}}
	_, err := CompileSpans(doc, "p", 30*24*time.Hour)
	ce, _ := err.(*CompileError)
	if ce == nil || ce.Status != 400 {
		t.Fatalf("scores on spans should be 400, got %v", err)
	}
}
