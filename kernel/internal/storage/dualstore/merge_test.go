package dualstore

import (
	"encoding/json"
	"testing"
)

func spanDoc(id, start, end string) json.RawMessage {
	m := map[string]any{"project_id": "p1", "id": id, "start_time": start}
	if end != "" {
		m["end_time"] = end
	}
	b, _ := json.Marshal(m)
	return b
}

func order(rows []json.RawMessage) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		var m map[string]any
		_ = json.Unmarshal(r, &m)
		out[i], _ = m["id"].(string)
	}
	return out
}

// H1 regression: ordering by the COMPUTED field `duration` must sort by the actual
// end-start interval (recomputed from doc timestamps), not collapse to id-only. Before
// the fix, docLess parsed the dialect SQL expression as a doc field name → nil → the
// merge silently sorted by id ASC, returning the wrong order (and, after trim, the
// wrong rows).
func TestMergeOrderByComputedDuration(t *testing.T) {
	// durations: a=3s, b=1s, c=10s. id order (a,b,c) deliberately disagrees with any
	// duration order so an id-only fallback would be visibly wrong.
	scale := []json.RawMessage{
		spanDoc("a", "2026-01-01T00:00:00Z", "2026-01-01T00:00:03Z"),
		spanDoc("c", "2026-01-01T00:00:00Z", "2026-01-01T00:00:10Z"),
	}
	lite := []json.RawMessage{
		spanDoc("b", "2026-01-01T00:00:00Z", "2026-01-01T00:00:01Z"),
	}

	desc := MergeOrdered(scale, lite, []OrderKey{{Field: "duration", Desc: true}, {Field: "id"}})
	if got := order(desc); got[0] != "c" || got[1] != "a" || got[2] != "b" {
		t.Fatalf("duration DESC: want [c a b], got %v", got)
	}
	asc := MergeOrdered(scale, lite, []OrderKey{{Field: "duration", Desc: false}, {Field: "id"}})
	if got := order(asc); got[0] != "b" || got[1] != "a" || got[2] != "c" {
		t.Fatalf("duration ASC: want [b a c], got %v", got)
	}
}

// An open span (no end_time) has a nil duration; it must sort last for BOTH
// directions (NULLS LAST, matching the explicit clause the query compiles for both
// engines). A DESC null-first would drop scale's open spans off a trimmed page — the
// cross-engine strand the review caught.
func TestMergeOrderByDurationNullSortsLastBothDirs(t *testing.T) {
	open := []json.RawMessage{spanDoc("open", "2026-01-01T00:00:00Z", "")}
	closed := []json.RawMessage{spanDoc("closed", "2026-01-01T00:00:00Z", "2026-01-01T00:00:05Z")}
	for _, desc := range []bool{false, true} {
		rows := MergeOrdered(open, closed, []OrderKey{{Field: "duration", Desc: desc}, {Field: "id"}})
		if got := order(rows); got[0] != "closed" || got[1] != "open" {
			t.Fatalf("null duration must sort last (desc=%v): want [closed open], got %v", desc, got)
		}
	}
}

// M1/M2 regression: aggregation merge must classify columns by their compiled OP, not
// a name prefix. count_distinct must NOT be summed across a straddle (it is not
// additive), and a caller-controlled alias that looks like a group column ("g0") or a
// mergeable op ("count_x") must not be misclassified.
func TestMergeAggregationClassifiesByOp(t *testing.T) {
	// One group "svcA" present in BOTH stores → a straddle for its aggregates.
	scale := []map[string]any{{"g0": "svcA", "n": float64(10), "uniq": float64(7), "total": float64(3)}}
	lite := []map[string]any{{"g0": "svcA", "n": float64(4), "uniq": float64(5), "total": float64(2)}}
	spec := AggSpec{
		GroupCols: []string{"g0"},
		Ops:       map[string]string{"n": "count", "uniq": "count_distinct", "total": "sum"},
	}
	rows, nonMergeable := MergeAggregation(scale, lite, spec)
	if len(rows) != 1 {
		t.Fatalf("want 1 merged group, got %d", len(rows))
	}
	r := rows[0]
	if r["n"] != float64(14) { // count is additive
		t.Fatalf("count must sum: want 14, got %v", r["n"])
	}
	if r["total"] != float64(5) { // sum is additive
		t.Fatalf("sum must add: want 5, got %v", r["total"])
	}
	if r["uniq"] != float64(7) { // count_distinct kept scale's partial, NOT summed to 12
		t.Fatalf("count_distinct must NOT be summed across a straddle: want 7 (scale), got %v", r["uniq"])
	}
	if len(nonMergeable) != 1 || nonMergeable[0] != "uniq" {
		t.Fatalf("count_distinct straddle must be reported: got %v", nonMergeable)
	}
}

// A caller aliasing an aggregate "g0" must not be swallowed into the group key — the
// spec's GroupCols comes from the compiler, so only real group columns group.
func TestMergeAggregationAliasCollisionSafe(t *testing.T) {
	scale := []map[string]any{{"g0": "svcA", "count_x": float64(1)}}
	lite := []map[string]any{{"g0": "svcA", "count_x": float64(1)}}
	// "count_x" is an avg (non-mergeable) despite its name; classification is by op.
	spec := AggSpec{GroupCols: []string{"g0"}, Ops: map[string]string{"count_x": "avg"}}
	rows, nonMergeable := MergeAggregation(scale, lite, spec)
	if len(rows) != 1 || rows[0]["count_x"] != float64(1) {
		t.Fatalf("name-prefix must not force a sum: got %v", rows)
	}
	if len(nonMergeable) != 1 || nonMergeable[0] != "count_x" {
		t.Fatalf("avg straddle must be reported non-mergeable: got %v", nonMergeable)
	}
}
