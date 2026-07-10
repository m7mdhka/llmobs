package postgres

import (
	"encoding/json"
	"math/rand"
	"testing"
	"time"
)

// TestMergeEventOrderIndependence is the adapter-level conformance for the
// per-field provenance rework (issue #17): applying a shuffled event sequence
// incrementally via MergeEvent (the read-modify-write the lite adapter runs) MUST
// equal the pure Fold over the whole set. This proves out-of-order updates fold
// identically — the meta-decision (cross-adapter identical semantics) holds.
func TestMergeEventOrderIndependence(t *testing.T) {
	ts := func(n int) time.Time { return time.Unix(int64(n), 0).UTC() }
	up := func(n int, eid string, p map[string]any) Event {
		return Event{Op: OpUpsert, EventTS: ts(n), EventID: eid, Payload: p}
	}
	events := []Event{
		up(1, "a", map[string]any{"id": "s1", "project_id": "p", "kind": "generation", "start_time": "100", "name": "a", "attributes": map[string]any{"x": 1.0}, "provided_usage_details": map[string]any{"input": 10.0}}),
		up(3, "c", map[string]any{"name": "c", "attributes": map[string]any{"y": 2.0}, "provided_usage_details": map[string]any{"output": 5.0}}),
		up(2, "b", map[string]any{"name": "b", "attributes": map[string]any{"x": 9.0}}),            // out-of-order
		up(6, "f", map[string]any{"kind": "tool_call", "status": map[string]any{"code": "error"}}), // frozen kind conflict
		up(2, "z", map[string]any{"events": []any{map[string]any{"name": "ev1", "timestamp": 2.0, "attributes": map[string]any{}}}}),
		{Op: OpDelete, EventTS: ts(4), EventID: "d"},
		up(5, "e", map[string]any{"output": "done"}), // revive after delete
	}

	want := canon(Fold("span", events))

	rng := rand.New(rand.NewSource(42))
	for iter := 0; iter < 300; iter++ {
		perm := append([]Event(nil), events...)
		rng.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })

		state := map[string]any{}
		prov := Provenance{}
		for _, ev := range perm {
			state, prov = MergeEvent("span", state, prov, ev)
			// round-trip provenance through JSON, as the adapter persists it
			prov = roundTripProv(t, prov)
			state = roundTripState(t, state)
		}
		if got := canon(state); got != want {
			t.Fatalf("shuffle %d diverged from Fold:\n got: %s\nwant: %s", iter, got, want)
		}
	}
}

func roundTripProv(t *testing.T, p Provenance) Provenance {
	t.Helper()
	b, _ := json.Marshal(p)
	var out Provenance
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("provenance round-trip: %v", err)
	}
	return out
}

func roundTripState(t *testing.T, s map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(s)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("state round-trip: %v", err)
	}
	return out
}

func canon(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
