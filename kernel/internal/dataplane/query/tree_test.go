package query

import "testing"

func sp(id, parent, start string) map[string]any {
	return map[string]any{"id": id, "parent_span_id": parent, "start_time": start}
}

func ids(spans []map[string]any) []string {
	out := make([]string, len(spans))
	for i, s := range spans {
		out[i], _ = s["id"].(string)
	}
	return out
}

// Preorder: parent before children; sibling order follows input (start_time,id).
func TestTreeOrderPreorder(t *testing.T) {
	in := []map[string]any{
		sp("root", "", "1"),
		sp("a", "root", "2"),
		sp("a1", "a", "3"),
		sp("b", "root", "4"),
	}
	got := ids(treeOrder(in))
	want := []string{"root", "a", "a1", "b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("preorder mismatch: got %v want %v", got, want)
		}
	}
}

// An orphan (parent not in the trace) is promoted to a root, never dropped.
func TestTreeOrderOrphanPromoted(t *testing.T) {
	in := []map[string]any{
		sp("root", "", "1"),
		sp("orphan", "missing-parent", "2"),
	}
	got := ids(treeOrder(in))
	if len(got) != 2 {
		t.Fatalf("orphan dropped: %v", got)
	}
}

// A parent/child cycle must not loop forever and must include every span once.
func TestTreeOrderCycleGuard(t *testing.T) {
	in := []map[string]any{
		sp("x", "y", "1"),
		sp("y", "x", "2"),
	}
	got := ids(treeOrder(in))
	if len(got) != 2 {
		t.Fatalf("cycle guard lost/duplicated spans: %v", got)
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("span %s emitted twice: %v", id, got)
		}
		seen[id] = true
	}
}

// synthesizeTrace spans earliest start to latest end and carries root dimensions.
func TestSynthesizeTrace(t *testing.T) {
	spans := []map[string]any{
		{"id": "root", "parent_span_id": "", "start_time": "2026-01-01T00:00:00Z", "end_time": "2026-01-01T00:00:05Z", "environment": "prod"},
		{"id": "child", "parent_span_id": "root", "start_time": "2026-01-01T00:00:01Z", "end_time": "2026-01-01T00:00:09Z"},
	}
	tr := synthesizeTrace("p1", "t1", spans)
	if tr["id"] != "t1" || tr["project_id"] != "p1" {
		t.Fatalf("identity wrong: %v", tr)
	}
	if tr["start_time"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("start should be earliest: %v", tr["start_time"])
	}
	if tr["end_time"] != "2026-01-01T00:00:09Z" {
		t.Fatalf("end should be latest: %v", tr["end_time"])
	}
	if tr["environment"] != "prod" {
		t.Fatalf("environment should come from root: %v", tr["environment"])
	}
}
