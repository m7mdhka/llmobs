package postgres

import "github.com/m7mdhka/llmobs/kernel/internal/storage"

// Conformer exposes the adapter's normative-merge behavior to the shared
// conformance harness (tools/conformance). It has no DB dependency: the merge
// fold is a pure function, so the harness runs the spec's V-vectors and the
// order-independence property without a database.
type Conformer struct{}

var _ storage.MergeConformer = Conformer{}

func (Conformer) Name() string { return "postgres" }

// Fold is the pure ordered fold (the normative reference).
func (Conformer) Fold(entityType string, events []storage.Event) map[string]any {
	return Fold(entityType, events)
}

// MergeIncremental applies events one-by-one via the read-modify-write path
// (MergeEvent against accumulated state+provenance), the way the lite adapter
// folds under the row lock. It MUST equal Fold over the same set for any order.
func (Conformer) MergeIncremental(entityType string, events []storage.Event) map[string]any {
	state := map[string]any{}
	prov := Provenance{}
	for _, ev := range events {
		state, prov = MergeEvent(entityType, state, prov, ev)
	}
	return state
}
