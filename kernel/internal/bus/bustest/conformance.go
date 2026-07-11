// Package bustest holds the event-bus conformance suite so the SAME proofs run
// against EVERY bus.Store backend — the in-memory store, Postgres-lite, and the
// Redis/Valkey scale backend. The contract is the interface, not the backend
// (the bus analogue of the cross-adapter storage conformance): H6 backlog-replay,
// at-least-once, backlog-cap→DLQ, and tenant/topic isolation all live here, driven
// through the shared *bus.Bus, so a new backend proves itself by passing them.
package bustest

import (
	"context"
	"testing"

	"github.com/m7mdhka/llmobs/kernel/internal/bus"
)

// Factory builds a fresh, empty Bus over one backend, plus a closure returning the
// backend's dead-letter-record count. It registers any cleanup via t.
type Factory func(t *testing.T, backlogCap int64) (*bus.Bus, func() int)

const proj = "projA"

func pub(t *testing.T, b *bus.Bus, topic, subject string) {
	t.Helper()
	if err := b.Publish(context.Background(), topic, proj, subject); err != nil {
		t.Fatal(err)
	}
}

// RunConformance runs every bus contract proof against the backend built by f.
func RunConformance(t *testing.T, f Factory) {
	t.Run("H6ReplayFromOffset", func(t *testing.T) { replayFromOffset(t, f) })
	t.Run("BacklogCapDeadLetters", func(t *testing.T) { backlogCapDeadLetters(t, f) })
	t.Run("TenantAndTopicIsolation", func(t *testing.T) { tenantAndTopicIsolation(t, f) })
	t.Run("MultiTopicPollAndMax", func(t *testing.T) { multiTopicPollAndMax(t, f) })
}

// replayFromOffset is the H6 backlog-replay proof: a subscriber that was DOWN while
// events were published replays them from its offset on the next poll; polling
// again without ack re-delivers (at-least-once); acking stops re-delivery; a new
// event after the ack is delivered.
func replayFromOffset(t *testing.T, f Factory) {
	b, _ := f(t, 1000)
	ctx := context.Background()

	pub(t, b, "span.ingested", "s1")
	pub(t, b, "span.ingested", "s2")
	pub(t, b, "span.ingested", "s3")

	got, err := b.Poll(ctx, "acme/w", proj, []string{"span.ingested"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].SubjectID != "s1" || got[2].SubjectID != "s3" {
		t.Fatalf("replay from offset failed: %+v", got)
	}

	again, _ := b.Poll(ctx, "acme/w", proj, []string{"span.ingested"}, 100)
	if len(again) != 3 {
		t.Fatalf("unacked events must be re-delivered, got %d", len(again))
	}

	if err := b.Ack(ctx, "acme/w", proj, "span.ingested", got[2].ID); err != nil {
		t.Fatal(err)
	}
	after, _ := b.Poll(ctx, "acme/w", proj, []string{"span.ingested"}, 100)
	if len(after) != 0 {
		t.Fatalf("acked events must not re-deliver, got %d", len(after))
	}

	pub(t, b, "span.ingested", "s4")
	tail, _ := b.Poll(ctx, "acme/w", proj, []string{"span.ingested"}, 100)
	if len(tail) != 1 || tail[0].SubjectID != "s4" {
		t.Fatalf("new event after ack: %+v", tail)
	}
}

// backlogCapDeadLetters: a subscriber that falls further behind than the cap has
// its overflow dead-lettered and its offset jumped forward, so it still receives
// the most-recent cap events (the newest ids), and a dead subscriber cannot pin
// the log.
func backlogCapDeadLetters(t *testing.T, f Factory) {
	b, dlqLen := f(t, 5) // cap = 5
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
	if dlqLen() == 0 {
		t.Fatal("overflow beyond the cap must be dead-lettered")
	}
	if got[0].ID != 16 || got[4].ID != 20 {
		t.Fatalf("expected the newest 5 (ids 16..20), got %d..%d", got[0].ID, got[4].ID)
	}
}

// tenantAndTopicIsolation: events are scoped by (project, topic).
func tenantAndTopicIsolation(t *testing.T, f Factory) {
	b, _ := f(t, 1000)
	ctx := context.Background()
	pub(t, b, "span.ingested", "a")
	pub(t, b, "score.created", "b")
	if err := b.Publish(ctx, "span.ingested", "otherproj", "c"); err != nil {
		t.Fatal(err)
	}

	got, _ := b.Poll(ctx, "acme/w", proj, []string{"span.ingested"}, 100)
	if len(got) != 1 || got[0].SubjectID != "a" {
		t.Fatalf("topic/tenant isolation failed: %+v", got)
	}
	other, _ := b.Poll(ctx, "acme/w", "otherproj", []string{"span.ingested"}, 100)
	if len(other) != 1 || other[0].SubjectID != "c" {
		t.Fatalf("cross-project isolation failed: %+v", other)
	}
}

// multiTopicPollAndMax: multi-topic fan-out plus the max clamp.
func multiTopicPollAndMax(t *testing.T, f Factory) {
	b, _ := f(t, 1000)
	ctx := context.Background()
	pub(t, b, "t1", "a")
	pub(t, b, "t2", "b")

	got, _ := b.Poll(ctx, "acme/w", proj, []string{"t1", "t2"}, 100)
	if len(got) != 2 {
		t.Fatalf("multi-topic poll should return both, got %d", len(got))
	}
	one, _ := b.Poll(ctx, "acme/w", proj, []string{"t1", "t2"}, 1)
	if len(one) != 1 {
		t.Fatalf("max clamp failed, got %d", len(one))
	}
}
