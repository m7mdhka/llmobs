package redisstore_test

import (
	"context"
	"os"
	"testing"

	"github.com/m7mdhka/llmobs/kernel/internal/bus"
	"github.com/m7mdhka/llmobs/kernel/internal/bus/redisstore"
)

// TestStreamTrimBoundsLogWithoutDroppingDeliverable is the #98 proof: the Streams log is
// bounded (it does not grow without limit), AND the trim never drops backlog a subscriber
// can still consume. The retention is set to the bus backlog cap, and the bus dead-letters
// anything more than backlogCap behind latest — so the last `cap` entries (the deliverable
// window) must remain fully readable after far more than `cap` appends.
func TestStreamTrimBoundsLogWithoutDroppingDeliverable(t *testing.T) {
	url := os.Getenv("LLMOBS_REDIS_TEST_URL")
	if url == "" {
		t.Skip("set LLMOBS_REDIS_TEST_URL to run the Redis/Valkey trim test")
	}
	ctx := context.Background()
	rdb, err := redisstore.Dial(ctx, redisstore.Config{URL: url})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flushdb: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })

	const cap = 50
	s := redisstore.New(rdb, "llmobs")
	s.SetStreamMaxLen(cap)

	// Append far more than the cap.
	const total = 500
	for i := 0; i < total; i++ {
		if _, err := s.Append(ctx, "span.ingested", "proj", "subj"); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	// 1) BOUNDED: the physical stream is far below `total` — approximate trimming keeps
	// AT LEAST `cap` but nowhere near 500 (a naive no-trim log would hold all 500).
	n, err := rdb.XLen(ctx, "llmobs:log:proj:span.ingested").Result()
	if err != nil {
		t.Fatalf("xlen: %v", err)
	}
	if n >= total {
		t.Fatalf("stream not trimmed: %d entries after %d appends (unbounded growth)", n, total)
	}
	if n < cap {
		t.Fatalf("stream over-trimmed: %d < cap %d — the deliverable window was cut", n, cap)
	}

	// 2) DELIVERABLE PRESERVED: a fresh subscriber, driven through the bus (which
	// dead-letters the >cap-behind overflow and reads the rest), receives the full
	// deliverable window — the last `cap` events — with none dropped by the trim.
	b := bus.New(s, cap)
	got, err := b.Poll(ctx, "acme/w", "proj", []string{"span.ingested"}, 1000)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(got) != cap {
		t.Fatalf("deliverable window = %d events, want %d (the last cap entries must survive the trim)", len(got), cap)
	}
	// The delivered ids must be the CONTIGUOUS tail [total-cap+1 .. total] — no gap the
	// trim punched into the middle of the deliverable window.
	wantFirst := int64(total - cap + 1)
	if got[0].ID != wantFirst || got[len(got)-1].ID != int64(total) {
		t.Fatalf("deliverable window ids = [%d..%d], want [%d..%d]", got[0].ID, got[len(got)-1].ID, wantFirst, total)
	}
}
