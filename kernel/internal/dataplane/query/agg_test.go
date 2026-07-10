package query

import (
	"strings"
	"testing"
	"time"
)

func compileAgg(t *testing.T, target string, doc map[string]any) *CompiledAgg {
	t.Helper()
	ca, err := CompileAggregation(doc, "p", 30*24*time.Hour, target)
	if err != nil {
		t.Fatalf("compile agg: %v", err)
	}
	return ca
}

// count/sum/percentile + groupBy field + time bucket — the cost-by-model-by-day shape.
func TestAggregationCostByModelByDay(t *testing.T) {
	doc := map[string]any{
		"target":    "spans",
		"timeRange": map[string]any{"from": "2026-01-01T00:00:00Z", "to": "2026-01-08T00:00:00Z"},
		"groupBy": []any{
			"model",
			map[string]any{"field": "start_time", "interval": "1d"},
		},
		"aggregations": []any{
			map[string]any{"op": "count"},
			map[string]any{"op": "sum", "field": "total_cost", "alias": "cost"},
			map[string]any{"op": "p95", "field": "total_cost"},
		},
	}
	ca := compileAgg(t, "spans", doc)
	if !strings.Contains(ca.Select, "model AS g0") {
		t.Fatalf("group field missing: %s", ca.Select)
	}
	if !strings.Contains(ca.Select, "date_bin('1 day', start_time") {
		t.Fatalf("time bucket missing: %s", ca.Select)
	}
	if !strings.Contains(ca.Select, `COUNT(*) AS "count"`) {
		t.Fatalf("count missing: %s", ca.Select)
	}
	if !strings.Contains(ca.Select, `SUM(total_cost)::float8 AS "cost"`) {
		t.Fatalf("sum alias missing: %s", ca.Select)
	}
	if !strings.Contains(ca.Select, "PERCENTILE_CONT(0.95)") {
		t.Fatalf("p95 missing: %s", ca.Select)
	}
	if ca.GroupBy != "g0, g1" {
		t.Fatalf("group by aliases wrong: %s", ca.GroupBy)
	}
}

// numeric map-key aggregation is jsonb_typeof-guarded (bad data excluded).
func TestAggregationNumericMapKey(t *testing.T) {
	ca := compileAgg(t, "spans", map[string]any{
		"target":       "spans",
		"timeRange":    map[string]any{"from": "2026-01-01T00:00:00Z", "to": "2026-01-02T00:00:00Z"},
		"aggregations": []any{map[string]any{"op": "avg", "field": "usage_details", "key": "input"}},
	})
	if !strings.Contains(ca.Select, "jsonb_typeof(usage_details -> 'input') = 'number'") {
		t.Fatalf("map cast not guarded: %s", ca.Select)
	}
}

func TestAggregationRejections(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"target":       "spans",
			"timeRange":    map[string]any{"from": "2026-01-01T00:00:00Z", "to": "2026-01-02T00:00:00Z"},
			"aggregations": []any{map[string]any{"op": "count"}},
		}
	}
	// sum on a string field -> operator_not_allowed
	d := base()
	d["aggregations"] = []any{map[string]any{"op": "sum", "field": "name"}}
	assertAggErr(t, d, "operator_not_allowed", 422)
	// group by a high-cardinality field -> not_groupable
	d = base()
	d["groupBy"] = []any{"trace_id"}
	assertAggErr(t, d, "not_groupable", 422)
	// two time buckets -> schema_invalid
	d = base()
	d["groupBy"] = []any{
		map[string]any{"field": "start_time", "interval": "1m"},
		map[string]any{"field": "start_time", "interval": "1h"},
	}
	assertAggErr(t, d, "schema_invalid", 400)
	// orderBy present -> schema_invalid
	d = base()
	d["orderBy"] = []any{map[string]any{"field": "start_time"}}
	assertAggErr(t, d, "schema_invalid", 400)
	// too many aggregations
	d = base()
	many := make([]any, 11)
	for i := range many {
		many[i] = map[string]any{"op": "count"}
	}
	d["aggregations"] = many
	assertAggErr(t, d, "schema_invalid", 400)
}

func assertAggErr(t *testing.T, doc map[string]any, code string, status int) {
	t.Helper()
	_, err := CompileAggregation(doc, "p", 30*24*time.Hour, "spans")
	ce, _ := err.(*CompileError)
	if ce == nil || ce.Code != code || ce.Status != status {
		t.Fatalf("want %s/%d, got %v", code, status, err)
	}
}
