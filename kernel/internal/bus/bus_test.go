package bus

import (
	"context"
	"testing"
)

// busFactory builds a fresh Bus for the contract suite. Adding a Postgres/Redis
// factory here (when a test DB is available) runs the SAME proofs against those
// backends — the contract is the interface, not the backend.
type busFactory struct {
	name string
	make func(cap int64) (*Bus, *MemStore)
}

func memFactory() busFactory {
	return busFactory{name: "mem", make: func(c int64) (*Bus, *MemStore) {
		ms := NewMemStore()
		return New(ms, c), ms
	}}
}

const proj = "projA"

func pub(t *testing.T, b *Bus, topic, subject string) {
	t.Helper()
	if err := b.Publish(context.Background(), topic, proj, subject); err != nil {
		t.Fatal(err)
	}
}

// TestReplayFromOffset is the H6 backlog-replay proof, written against the Bus
// interface so ANY backend passes it: a subscriber that was DOWN while events were
// published replays them from its offset on the next poll, then advancing its
// offset (ack) stops re-delivery.
func TestReplayFromOffset(t *testing.T) {
	f := memFactory()
	b, _ := f.make(1000)
	ctx := context.Background()

	// Subscriber is "down" — no polling. Three events are published.
	pub(t, b, "span.ingested", "s1")
	pub(t, b, "span.ingested", "s2")
	pub(t, b, "span.ingested", "s3")

	// Subscriber comes back and polls: it REPLAYS the whole backlog from offset 0.
	got, err := b.Poll(ctx, "acme/w", proj, []string{"span.ingested"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].SubjectID != "s1" || got[2].SubjectID != "s3" {
		t.Fatalf("replay from offset failed: %+v", got)
	}

	// Poll again WITHOUT ack — at-least-once: the same events are re-delivered.
	again, _ := b.Poll(ctx, "acme/w", proj, []string{"span.ingested"}, 100)
	if len(again) != 3 {
		t.Fatalf("unacked events must be re-delivered, got %d", len(again))
	}

	// Ack up to the last id — now they are not re-delivered.
	if err := b.Ack(ctx, "acme/w", proj, "span.ingested", got[2].ID); err != nil {
		t.Fatal(err)
	}
	after, _ := b.Poll(ctx, "acme/w", proj, []string{"span.ingested"}, 100)
	if len(after) != 0 {
		t.Fatalf("acked events must not re-deliver, got %d", len(after))
	}

	// A new event after the ack is delivered.
	pub(t, b, "span.ingested", "s4")
	tail, _ := b.Poll(ctx, "acme/w", proj, []string{"span.ingested"}, 100)
	if len(tail) != 1 || tail[0].SubjectID != "s4" {
		t.Fatalf("new event after ack: %+v", tail)
	}
}

// TestBacklogCapDeadLetters: a subscriber that falls further behind than the cap
// has its overflow dead-lettered and its offset jumped forward, so a dead
// subscriber cannot pin the log; it still receives the most-recent `cap` events.
func TestBacklogCapDeadLetters(t *testing.T) {
	b, ms := memFactory().make(5) // cap = 5
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		pub(t, b, "span.ingested", "s")
	}
	got, err := b.Poll(ctx, "acme/w", proj, []string{"span.ingested"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("subscriber should get the most-recent cap(=5) events, got %d", len(got))
	}
	// The 15 overflow events were dead-lettered (one range record).
	if ms.DLQLen() == 0 {
		t.Fatal("overflow beyond the cap must be dead-lettered")
	}
	// It got the NEWEST 5 (ids 16..20).
	if got[0].ID != 16 || got[4].ID != 20 {
		t.Fatalf("expected the newest 5, got ids %d..%d", got[0].ID, got[4].ID)
	}
}

// TestTenantAndTopicIsolation: events are scoped by (project, topic) — a
// subscriber never sees another project's or another topic's events.
func TestTenantAndTopicIsolation(t *testing.T) {
	b, _ := memFactory().make(1000)
	ctx := context.Background()
	_ = b.Publish(ctx, "span.ingested", "projA", "a1")
	_ = b.Publish(ctx, "span.ingested", "projB", "b1")
	_ = b.Publish(ctx, "score.created", "projA", "sc1")

	// projA subscriber on span.ingested sees only a1.
	got, _ := b.Poll(ctx, "acme/w", "projA", []string{"span.ingested"}, 100)
	if len(got) != 1 || got[0].SubjectID != "a1" {
		t.Fatalf("tenant/topic isolation failed: %+v", got)
	}
}

// TestMultiTopicPollAndMax: a poll across topics respects the max and each topic's
// own offset.
func TestMultiTopicPollAndMax(t *testing.T) {
	b, _ := memFactory().make(1000)
	ctx := context.Background()
	pub(t, b, "span.ingested", "s1")
	_ = b.Publish(ctx, "score.created", proj, "sc1")
	got, _ := b.Poll(ctx, "acme/w", proj, []string{"span.ingested", "score.created"}, 100)
	if len(got) != 2 {
		t.Fatalf("multi-topic poll should return both, got %d", len(got))
	}
	// max caps the batch.
	pub(t, b, "span.ingested", "s2")
	pub(t, b, "span.ingested", "s3")
	capped, _ := b.Poll(ctx, "acme/w2", proj, []string{"span.ingested"}, 2)
	if len(capped) != 2 {
		t.Fatalf("max should cap the batch to 2, got %d", len(capped))
	}
}
