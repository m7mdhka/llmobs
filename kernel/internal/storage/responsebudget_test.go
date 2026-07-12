package storage

import (
	"context"
	"errors"
	"testing"
)

// TestResponseBudget is the hermetic proof of the shared #83 mechanism both adapters
// enforce. It is deliberately in-package (white-box) so a reviewer sees the exact
// accounting: the running total is checked per Add, the ceiling is inclusive (equal is
// OK, one byte over refuses), and a disabled budget is a true no-op.
func TestResponseBudget(t *testing.T) {
	t.Run("refuses once the running total exceeds the ceiling", func(t *testing.T) {
		b := NewResponseBudget(WithResponseBudget(context.Background(), 100))
		if err := b.Add(60); err != nil {
			t.Fatalf("first 60 bytes must fit under 100: %v", err)
		}
		if err := b.Add(40); err != nil { // exactly at 100 — inclusive, still OK
			t.Fatalf("reaching the ceiling exactly must be allowed: %v", err)
		}
		if err := b.Add(1); !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("one byte past the ceiling must return ErrResponseTooLarge, got %v", err)
		}
	})

	t.Run("a single oversized row is refused (no unbounded first row)", func(t *testing.T) {
		// The failure mode #83 targets: a page whose FIRST wide-payload row already blows the
		// budget must be refused before it is retained — the adapter never buffers past ceiling.
		b := NewResponseBudget(WithResponseBudget(context.Background(), 1024))
		if err := b.Add(1_000_000); !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("a single 1MB row against a 1KB ceiling must refuse, got %v", err)
		}
	})

	t.Run("absent budget is unbounded (internal callers opt out)", func(t *testing.T) {
		// Backfill/conformance callers don't set a budget; Add must be a no-op so a scan of a
		// large historical set is never capped for a non-HTTP path.
		b := NewResponseBudget(context.Background())
		for i := 0; i < 1000; i++ {
			if err := b.Add(1_000_000); err != nil {
				t.Fatalf("unbounded budget must never refuse, refused at i=%d: %v", i, err)
			}
		}
	})

	t.Run("explicit non-positive ceiling disables the bound", func(t *testing.T) {
		b := NewResponseBudget(WithResponseBudget(context.Background(), 0))
		if err := b.Add(1_000_000); err != nil {
			t.Fatalf("a 0 ceiling must disable the bound, got %v", err)
		}
	})
}
