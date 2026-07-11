package clickhouse

import (
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/merge"
)

// Conformer exposes the ClickHouse adapter's normative-merge behavior to the
// shared conformance harness. Like the lite adapter it has no DB dependency: the
// merge fold is the SAME shared pure function (kernel/internal/storage/merge), so
// the harness proves both adapters fold identically. That the two Conformers
// delegate to one implementation is the point — a divergence would be a compile
// error, not a silent correctness bug.
type Conformer struct{}

var _ storage.MergeConformer = Conformer{}

func (Conformer) Name() string { return "clickhouse" }

// Fold is the pure ordered fold (the normative reference).
func (Conformer) Fold(entityType string, events []storage.Event) map[string]any {
	return merge.Fold(entityType, events)
}

// MergeIncremental applies events one-by-one via the read-modify-write path — the
// way the scale adapter folds each event against the current settled row — and
// MUST equal Fold over the same set for any order.
func (Conformer) MergeIncremental(entityType string, events []storage.Event) map[string]any {
	state := map[string]any{}
	prov := merge.Provenance{}
	for _, ev := range events {
		state, prov = merge.MergeEvent(entityType, state, prov, ev)
	}
	return state
}
