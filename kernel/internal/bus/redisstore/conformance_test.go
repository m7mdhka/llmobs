package redisstore_test

import (
	"context"
	"os"
	"testing"

	"github.com/m7mdhka/llmobs/kernel/internal/bus"
	"github.com/m7mdhka/llmobs/kernel/internal/bus/bustest"
	"github.com/m7mdhka/llmobs/kernel/internal/bus/redisstore"
)

// TestRedisConformance is the load-bearing proof: the SAME event-bus conformance
// suite the in-memory + Postgres backends pass runs GREEN against the Redis/Valkey
// Streams scale backend — the event-bus analogue of the cross-adapter storage
// proof. Backlog-replay, at-least-once, backlog-cap→DLQ, and tenant/topic
// isolation are all driven through the shared bus.Bus over the real backend.
//
// Env-gated on LLMOBS_REDIS_TEST_URL (a real Redis/Valkey); skips otherwise.
func TestRedisConformance(t *testing.T) {
	url := os.Getenv("LLMOBS_REDIS_TEST_URL")
	if url == "" {
		t.Skip("set LLMOBS_REDIS_TEST_URL to run the Redis/Valkey conformance")
	}
	ctx := context.Background()

	bustest.RunConformance(t, func(t *testing.T, cap int64) (*bus.Bus, func() int) {
		rdb, err := redisstore.Dial(ctx, redisstore.Config{URL: url})
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		// Each conformance case gets a clean keyspace (tests run serially).
		if err := rdb.FlushDB(ctx).Err(); err != nil {
			t.Fatalf("flushdb: %v", err)
		}
		t.Cleanup(func() { _ = rdb.Close() })
		s := redisstore.New(rdb, "llmobs")
		return bus.New(s, cap), func() int {
			n, _ := s.DLQLen(ctx)
			return n
		}
	})
}
