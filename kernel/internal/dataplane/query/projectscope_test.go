package query

import (
	"testing"
	"time"
)

// TestProjectScopeIsFirstArg locks the compiler contract the ClickHouse adapter
// depends on for tenant isolation: project scoping is ALWAYS predicate #1, so
// Args[0] is the tenant id. The CH adapter reuses Args[0] to push project_id into
// its settled-row dedup subquery; if a future compiler change bound a different
// value first, the CH scan would silently mis-scope while Postgres (which consumes
// the WHERE text) stayed correct. This test turns that silent break into a failure.
func TestProjectScopeIsFirstArg(t *testing.T) {
	const pid = "proj-scope-lock"
	tr := map[string]any{
		"from": "2026-01-01T00:00:00Z",
		"to":   "2026-01-02T00:00:00Z",
	}
	window := 30 * 24 * time.Hour

	t.Run("spans", func(t *testing.T) {
		for _, d := range []Dialect{PostgresDialect, ClickHouseDialect} {
			c, err := CompileSpansForDialect(map[string]any{"target": "spans", "timeRange": tr}, pid, window, d)
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Args) == 0 || c.Args[0] != pid {
				t.Fatalf("%s: Args[0]=%v, want project id %q as predicate #1", d.name(), firstArg(c.Args), pid)
			}
		}
	})
	t.Run("scores", func(t *testing.T) {
		c, err := CompileScoresForDialect(map[string]any{"target": "scores", "timeRange": tr}, pid, window, ClickHouseDialect)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Args) == 0 || c.Args[0] != pid {
			t.Fatalf("scores: Args[0]=%v, want %q", firstArg(c.Args), pid)
		}
	})
	t.Run("traces", func(t *testing.T) {
		c, err := CompileTracesForDialect(map[string]any{"target": "traces", "timeRange": tr}, pid, window, ClickHouseDialect)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Args) == 0 || c.Args[0] != pid {
			t.Fatalf("traces: Args[0]=%v, want %q", firstArg(c.Args), pid)
		}
	})
	t.Run("aggregation", func(t *testing.T) {
		doc := map[string]any{"target": "spans", "timeRange": tr, "aggregations": []any{map[string]any{"op": "count"}}}
		ca, err := CompileAggregationForDialect(doc, pid, window, "spans", ClickHouseDialect)
		if err != nil {
			t.Fatal(err)
		}
		if len(ca.Args) == 0 || ca.Args[0] != pid {
			t.Fatalf("aggregation: Args[0]=%v, want %q", firstArg(ca.Args), pid)
		}
	})
}

func firstArg(args []any) any {
	if len(args) == 0 {
		return nil
	}
	return args[0]
}
