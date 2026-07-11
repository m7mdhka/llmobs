package conformance

import (
	"testing"

	"github.com/m7mdhka/llmobs/kernel/internal/storage/clickhouse"
)

// The scale ClickHouse adapter runs through the SAME shared harness as the lite
// adapter (postgres_test.go). Both Conformers delegate to the one shared merge
// fold, so this proves the scale adapter folds byte-identically to lite — the
// cross-adapter meta-decision (ADR-0026), enforced from L1. SQL-level observable
// parity against a running ClickHouse is L2.
func TestClickHouseConformance(t *testing.T) {
	RunConformance(t, clickhouse.Conformer{})
}
