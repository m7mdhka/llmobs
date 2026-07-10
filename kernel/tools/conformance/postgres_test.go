package conformance

import (
	"testing"

	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

// The lite Postgres adapter runs through the shared harness — the source of
// conformance truth. A new in-tree adapter adds a sibling file registering its
// own storage.MergeConformer and calling RunConformance.
func TestPostgresConformance(t *testing.T) {
	RunConformance(t, postgres.Conformer{})
}
