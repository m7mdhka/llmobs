package query

import (
	"strings"
	"testing"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// TestFlagAggTruncation proves the loud-or-complete rule: an aggregation result at
// the cap is returned as-is (it MAY be exactly complete), but a result of cap+1 — the
// adapters' deliberate over-fetch signal — is trimmed back to the cap AND carries a loud
// warning, so a truncated (incomplete) aggregate is never returned as if it were complete.
func TestFlagAggTruncation(t *testing.T) {
	t.Run("exactly at cap: complete, no warning", func(t *testing.T) {
		g, w := flagAggTruncation(make([]map[string]any, storage.MaxAggregationGroups), nil)
		if len(g) != storage.MaxAggregationGroups {
			t.Fatalf("at cap must pass through %d groups, got %d", storage.MaxAggregationGroups, len(g))
		}
		if len(w) != 0 {
			t.Fatalf("at cap must NOT warn (it may be exactly complete), got %v", w)
		}
	})

	t.Run("over cap (the over-fetch signal): trimmed + loud warning", func(t *testing.T) {
		g, w := flagAggTruncation(make([]map[string]any, storage.MaxAggregationGroups+1), nil)
		if len(g) != storage.MaxAggregationGroups {
			t.Fatalf("over cap must trim to %d, got %d", storage.MaxAggregationGroups, len(g))
		}
		if len(w) != 1 {
			t.Fatalf("over cap must emit exactly one warning, got %d: %v", len(w), w)
		}
		if !strings.Contains(w[0], "truncated") || !strings.Contains(w[0], "INCOMPLETE") {
			t.Fatalf("the warning must loudly signal incompleteness, got %q", w[0])
		}
	})

	t.Run("under cap: untouched", func(t *testing.T) {
		g, w := flagAggTruncation(make([]map[string]any, 5), nil)
		if len(g) != 5 || len(w) != 0 {
			t.Fatalf("a small result must be untouched, got %d groups %d warnings", len(g), len(w))
		}
	})
}
