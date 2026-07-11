package crossadapter

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// costSpan seeds a span with a kind + optional total_cost in a given trace.
func costSpan(id, trace, parent, kind string, sec int, cost *float64) storage.Event {
	f := map[string]any{
		"trace_id": trace, "parent_span_id": parent, "kind": kind, "name": kind,
		"start_time": ts(sec).Format(time.RFC3339Nano), "end_time": ts(sec + 1).Format(time.RFC3339Nano),
		"model": "gpt-4o", "provider": "openai",
	}
	if cost != nil {
		f["total_cost"] = *cost
	}
	return spanEv(id, sec, f)
}

// traceCost extracts a trace's total_cost from QueryTraces output.
func traceCost(t *testing.T, rows []json.RawMessage, id string) (float64, bool) {
	t.Helper()
	for _, r := range rows {
		var m map[string]any
		if err := json.Unmarshal(r, &m); err != nil {
			t.Fatal(err)
		}
		if m["id"] == id {
			c, ok := m["total_cost"].(float64)
			return c, ok
		}
	}
	t.Fatalf("trace %s not found in %d rows", id, len(rows))
	return 0, false
}

// TestTraceCostNoDoubleCount is the M3 load-bearing proof (§7.1): trace-level total_cost
// over an ARBITRARY tree sums each token's cost exactly once — a parent agent_step whose
// cost duplicates its descendants is NEVER summed with them — and the roll-up is IDENTICAL
// on Postgres and ClickHouse (the money path + cross-adapter, the two bug-hiding categories).
func TestTraceCostNoDoubleCount(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	c := func(v float64) *float64 { return &v }

	// Trace "tnest": a MULTI-LEVEL agent tree where BOTH agent_step parents carry cost
	// that duplicates the leaf (the exact double-count trap). Trace cost must be the LEAF
	// only (0.03), never 0.03+0.05+0.05, never 2x.
	nest := []storage.Event{
		costSpan("na", "tnest", "", "agent_step", 20, c(0.05)),   // parent aggregate (duplicate)
		costSpan("nb", "tnest", "na", "agent_step", 21, c(0.05)), // nested aggregate (duplicate)
		costSpan("nc", "tnest", "nb", "tool_call", 22, c(0.0)),   // a tool step (no real cost)
		costSpan("nd", "tnest", "nb", "generation", 23, c(0.03)), // the leaf model call — the real cost
	}
	// Trace "tmulti": two leaf model calls under one agent → trace = sum of BOTH leaves.
	multi := []storage.Event{
		costSpan("ma", "tmulti", "", "agent_step", 30, c(0.09)),   // aggregate (sum of leaves, duplicate)
		costSpan("mb", "tmulti", "ma", "generation", 31, c(0.04)), // leaf 1
		costSpan("mc", "tmulti", "ma", "generation", 32, c(0.05)), // leaf 2
	}
	for _, ev := range append(append([]storage.Event{}, nest...), multi...) {
		if err := h.pg.PersistSpan(ctx, ev); err != nil {
			t.Fatalf("pg persist %v: %v", ev.EventID, err)
		}
		if err := h.ch.PersistSpan(ctx, ev); err != nil {
			t.Fatalf("ch persist %v: %v", ev.EventID, err)
		}
	}

	doc := map[string]any{"target": "traces", "timeRange": map[string]any{
		"from": ts(0).Format(time.RFC3339), "to": ts(100).Format(time.RFC3339),
	}}
	pg := runPG(t, h, "traces", doc)
	ch := runCH(t, h, "traces", doc)

	for _, tc := range []struct {
		trace string
		want  float64
	}{
		{"tnest", 0.03},  // leaf only — both agent_steps + the tool excluded
		{"tmulti", 0.09}, // both leaves (0.04+0.05); the agent's duplicate 0.09 NOT added on top
	} {
		pgCost, pgOK := traceCost(t, pg, tc.trace)
		chCost, chOK := traceCost(t, ch, tc.trace)
		if !pgOK || !chOK {
			t.Fatalf("%s: trace total_cost missing (pg=%v ch=%v)", tc.trace, pgOK, chOK)
		}
		if math.Abs(pgCost-tc.want) > 1e-9 {
			t.Fatalf("%s: PG trace cost=%v want %v (double-count?)", tc.trace, pgCost, tc.want)
		}
		// Cross-adapter identity (the load-bearing cross-adapter proof). EXACT float64
		// equality, not a tolerance: both engines round the roll-up to the shared scale
		// (storage.CostRoundDecimals), so PG's exact-NUMERIC sum and CH's float64 sum
		// collapse to the same value — e.g. tmulti's 0.04+0.05 is 0.09 on BOTH, not
		// 0.09000000000000001 on ClickHouse (the byte-divergence this closes).
		if pgCost != chCost {
			t.Fatalf("%s: trace cost diverges PG=%.17g vs CH=%.17g (must be byte-identical)", tc.trace, pgCost, chCost)
		}
		// Prove-the-negative: the naive full SUM (0.13 for tnest, 0.18 for tmulti) is what
		// double-counting would produce — assert we are NOT that.
		if math.Abs(pgCost-0.13) < 1e-9 || math.Abs(pgCost-0.18) < 1e-9 {
			t.Fatalf("%s: trace cost %v equals the double-counted full sum — aggregates were summed", tc.trace, pgCost)
		}
	}
}

// TestTraceCostNullWhenNoLeafCost proves a trace whose only cost is on an aggregate span
// (or none) reports total_cost NULL (absent) — identically on both engines — not 0 or the
// aggregate's value.
func TestTraceCostNullWhenNoLeafCost(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	c := func(v float64) *float64 { return &v }
	// Only an agent_step carries cost; its leaf tool has none. Trace cost must be NULL.
	evs := []storage.Event{
		costSpan("za", "tnull", "", "agent_step", 40, c(0.07)),
		costSpan("zb", "tnull", "za", "tool_call", 41, nil),
	}
	for _, ev := range evs {
		if err := h.pg.PersistSpan(ctx, ev); err != nil {
			t.Fatal(err)
		}
		if err := h.ch.PersistSpan(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	doc := map[string]any{"target": "traces", "timeRange": map[string]any{
		"from": ts(0).Format(time.RFC3339), "to": ts(100).Format(time.RFC3339),
	}}
	if _, ok := traceCost(t, runPG(t, h, "traces", doc), "tnull"); ok {
		t.Fatal("PG: a trace with cost only on an aggregate span must report total_cost NULL")
	}
	if _, ok := traceCost(t, runCH(t, h, "traces", doc), "tnull"); ok {
		t.Fatal("CH: a trace with cost only on an aggregate span must report total_cost NULL")
	}
}

// TestTraceCostNullKindIncluded is the #3 regression: a cost-bearing span with NO kind
// (NULL in Postgres, ” in ClickHouse) must be treated as NON-aggregate and INCLUDED in
// the roll-up on BOTH engines — the PG COALESCE(kind,”) bridges the NULL-vs-” sentinel
// so a null-kind span isn't silently dropped from PG's sum while summed by CH.
func TestTraceCostNullKindIncluded(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	// A span with total_cost but NO "kind" key → PG stores NULL kind, CH stores ''.
	ev := spanEv("nk", 50, map[string]any{
		"trace_id": "tnk", "parent_span_id": "", "name": "x",
		"start_time": ts(50).Format(time.RFC3339Nano), "end_time": ts(51).Format(time.RFC3339Nano),
		"total_cost": 0.06,
	})
	if err := h.pg.PersistSpan(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if err := h.ch.PersistSpan(ctx, ev); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"target": "traces", "timeRange": map[string]any{
		"from": ts(0).Format(time.RFC3339), "to": ts(100).Format(time.RFC3339),
	}}
	pgCost, pgOK := traceCost(t, runPG(t, h, "traces", doc), "tnk")
	chCost, chOK := traceCost(t, runCH(t, h, "traces", doc), "tnk")
	if !pgOK || !chOK {
		t.Fatalf("null-kind cost span must be INCLUDED on both engines (pg=%v ch=%v)", pgOK, chOK)
	}
	if pgCost != chCost || math.Abs(pgCost-0.06) > 1e-9 {
		t.Fatalf("null-kind span cost must match: pg=%v ch=%v want 0.06", pgCost, chCost)
	}
}
