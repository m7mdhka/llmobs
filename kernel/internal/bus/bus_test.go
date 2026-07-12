package bus_test

import (
	"testing"

	"github.com/m7mdhka/llmobs/kernel/internal/bus"
	"github.com/m7mdhka/llmobs/kernel/internal/bus/bustest"
)

// TestMemConformance runs the shared event-bus conformance suite (backlog-replay,
// at-least-once, backlog-cap→DLQ, tenant/topic isolation) against the in-memory
// backend. The Redis/Valkey scale backend runs the IDENTICAL suite
// (internal/bus/redisstore) — the contract is the interface, not the backend.
func TestMemConformance(t *testing.T) {
	bustest.RunConformance(t, func(t *testing.T, cap int64) (*bus.Bus, func() int) {
		ms := bus.NewMemStore()
		return bus.New(ms, cap), ms.DLQLen
	})
}
