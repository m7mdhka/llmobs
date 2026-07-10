// Package conformance is the cross-adapter storage conformance harness. It runs
// the normative merge suite — the V-vectors from 05-update-semantics.md and the
// order-independence property — against any storage.MergeConformer, so every
// adapter (the lite Postgres adapter today; ClickHouse/Timescale tomorrow) is
// held to one source of conformance truth.
//
// An adapter author adds a `<adapter>_test.go` here that registers their
// conformer and calls RunConformance. The Postgres adapter's CI runs through this
// harness; `go test ./tools/conformance/...` is the single command.
package conformance

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// specRelPath is the single source of the normative merge vectors, parsed
// straight from the spec (no fixture duplication). Path is relative to this
// package dir: kernel/tools/conformance -> repo root -> api.
const specRelPath = "../../../api/model/v1alpha1/05-update-semantics.md"

// minVectors guards against the spec's vector blocks silently disappearing.
const minVectors = 17

type vectorEvent struct {
	Op      string         `json:"op"`
	EventTS float64        `json:"event_ts"`
	EventID string         `json:"event_id"`
	Payload map[string]any `json:"payload"`
}

type vector struct {
	Name   string         `json:"name"`
	Entity string         `json:"entity"`
	Note   string         `json:"note"`
	Events []vectorEvent  `json:"events"`
	Expect map[string]any `json:"expect"`
}

var jsonBlock = regexp.MustCompile("(?s)```json\\n(.*?)\\n```")

// RunConformance runs the full suite against one adapter.
func RunConformance(t *testing.T, c storage.MergeConformer) {
	t.Helper()
	t.Run("vectors/"+c.Name(), func(t *testing.T) { runVectors(t, c) })
	t.Run("order-independence/"+c.Name(), func(t *testing.T) { runOrderIndependence(t, c) })
}

// runVectors asserts the adapter's ordered Fold matches every spec V-vector.
func runVectors(t *testing.T, c storage.MergeConformer) {
	vs := loadVectors(t)
	if len(vs) < minVectors {
		t.Fatalf("expected >= %d vectors, parsed %d", minVectors, len(vs))
	}
	for _, v := range vs {
		v := v
		t.Run(v.Name, func(t *testing.T) {
			events := toEvents(v.Events)
			got := c.Fold(v.Entity, events)
			if canon(got) != canon(v.Expect) {
				t.Errorf("%s (%s)\n  expect: %s\n  got   : %s", v.Name, v.Note, canon(v.Expect), canon(got))
			}
		})
	}
}

// runOrderIndependence asserts incremental merge == ordered Fold for many
// shuffles of a sequence exercising frozen conflicts, out-of-order updates,
// delete/revive, deep-merge maps, and span events (the adapter-level guarantee
// behind issue #17).
func runOrderIndependence(t *testing.T, c storage.MergeConformer) {
	ts := func(n int) time.Time { return time.Unix(int64(n), 0).UTC() }
	up := func(n int, eid string, p map[string]any) storage.Event {
		return storage.Event{Op: storage.OpUpsert, EventTS: ts(n), EventID: eid, Payload: p}
	}
	events := []storage.Event{
		up(1, "a", map[string]any{"id": "s1", "project_id": "p", "kind": "generation", "start_time": "100", "name": "a", "attributes": map[string]any{"x": 1.0}, "provided_usage_details": map[string]any{"input": 10.0}}),
		up(3, "c", map[string]any{"name": "c", "attributes": map[string]any{"y": 2.0}, "provided_usage_details": map[string]any{"output": 5.0}}),
		up(2, "b", map[string]any{"name": "b", "attributes": map[string]any{"x": 9.0}}),
		up(6, "f", map[string]any{"kind": "tool_call", "status": map[string]any{"code": "error"}}),
		up(2, "z", map[string]any{"events": []any{map[string]any{"name": "ev1", "timestamp": 2.0, "attributes": map[string]any{}}}}),
		{Op: storage.OpDelete, EventTS: ts(4), EventID: "d"},
		up(5, "e", map[string]any{"output": "done"}),
	}
	want := canon(c.Fold("span", events))

	rng := rand.New(rand.NewSource(42))
	for iter := 0; iter < 300; iter++ {
		perm := append([]storage.Event(nil), events...)
		rng.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
		if got := canon(c.MergeIncremental("span", perm)); got != want {
			t.Fatalf("shuffle %d diverged from Fold:\n got: %s\nwant: %s", iter, got, want)
		}
	}
}

func toEvents(in []vectorEvent) []storage.Event {
	out := make([]storage.Event, len(in))
	for i, e := range in {
		out[i] = storage.Event{
			Op:      storage.Op(e.Op),
			EventTS: time.Unix(int64(e.EventTS), 0).UTC(),
			EventID: e.EventID,
			Payload: e.Payload,
		}
	}
	return out
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(specRelPath))
	if err != nil {
		t.Fatalf("reading spec %s: %v", specRelPath, err)
	}
	matches := jsonBlock.FindAllStringSubmatch(string(raw), -1)
	if len(matches) == 0 {
		t.Fatalf("no JSON vector blocks found in %s", specRelPath)
	}
	var vs []vector
	for _, m := range matches {
		var v vector
		if err := json.Unmarshal([]byte(m[1]), &v); err != nil {
			t.Fatalf("parsing vector block: %v\n%s", err, m[1])
		}
		vs = append(vs, v)
	}
	return vs
}

func canon(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
