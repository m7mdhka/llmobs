// Package bus is the durable event bus behind pipeline.EventBus (the `events`
// primitive substrate). The lite profile is Postgres-backed (a durable event log
// with per-subscriber consumer offsets) — NO new infrastructure: the lite promise
// is single-dependency, Postgres-only. Redis Streams is the deferred SCALE backend
// behind this same interface (parallel to Postgres-lite/ClickHouse-scale for
// storage); the plugin-facing events contract is identical across both because it
// is the interface, not the backend.
//
// The hard logic — replay-from-offset, at-least-once, per-subscriber backlog cap,
// dead-lettering — lives HERE, over a small Store interface, so the contract test
// validates ANY backend (in-memory now, Postgres in prod, Redis later). Durability
// comes from the Store's event log + offsets, never from LISTEN/NOTIFY (which is a
// latency-only wake): a subscriber that was down replays from its offset on the
// next poll regardless of whether it received a notification.
package bus

import "context"

// Delivered is one event handed to a subscriber. ID is the monotonic log offset
// and the idempotency key (at-least-once => the SDK dedupes on ID).
type Delivered struct {
	ID        int64  `json:"id"`
	Topic     string `json:"topic"`
	ProjectID string `json:"project_id"`
	SubjectID string `json:"subject_id"`
}

// Store is the bus's persistence boundary. A backend implements only this; the
// replay/at-least-once/cap/DLQ logic is shared in Bus.
type Store interface {
	// Append writes an event to the durable log and returns its offset id.
	Append(ctx context.Context, topic, projectID, subjectID string) (int64, error)
	// LatestID returns the highest event id for (topic, project), or 0.
	LatestID(ctx context.Context, topic, projectID string) (int64, error)
	// After returns up to limit events with id > afterID for (topic, project), ascending.
	After(ctx context.Context, topic, projectID string, afterID int64, limit int) ([]Delivered, error)
	// Offset returns a subscriber's last-acked offset (0 if never acked).
	Offset(ctx context.Context, pluginID, projectID, topic string) (int64, error)
	// SetOffset advances a subscriber's offset (monotonic; callers pass GREATEST).
	SetOffset(ctx context.Context, pluginID, projectID, topic string, id int64) error
	// DeadLetter records that (fromID, toID] were skipped for a subscriber.
	DeadLetter(ctx context.Context, pluginID, projectID, topic string, fromID, toID int64, reason string) error
}

// Bus is the backend-agnostic event bus.
type Bus struct {
	store      Store
	backlogCap int64
	notify     func(topic, projectID string) // optional low-latency wake (LISTEN/NOTIFY); nil ok
}

const defaultBacklogCap = 10000

// New builds a Bus over a Store. backlogCap <= 0 uses the default.
func New(store Store, backlogCap int64) *Bus {
	if backlogCap <= 0 {
		backlogCap = defaultBacklogCap
	}
	return &Bus{store: store, backlogCap: backlogCap}
}

// SetNotifier attaches a low-latency wake hook (e.g. Postgres NOTIFY). Optional;
// durability does not depend on it.
func (b *Bus) SetNotifier(fn func(topic, projectID string)) { b.notify = fn }

// Publish appends an event to the durable log (implements pipeline.EventBus).
func (b *Bus) Publish(ctx context.Context, topic, projectID, subjectID string) error {
	if _, err := b.store.Append(ctx, topic, projectID, subjectID); err != nil {
		return err
	}
	if b.notify != nil {
		b.notify(topic, projectID)
	}
	return nil
}

// Poll returns up to max events across the given topics for a subscriber, after
// its offset — WITHOUT advancing it (only Ack does, giving at-least-once). A
// subscriber that has fallen more than backlogCap behind has its overflow
// dead-lettered and its offset advanced, so one dead subscriber cannot pin the
// log. Replay is automatic: a subscriber that was down reads its backlog here.
func (b *Bus) Poll(ctx context.Context, pluginID, projectID string, topics []string, max int) ([]Delivered, error) {
	if max <= 0 || max > 1000 {
		max = 1000
	}
	var out []Delivered
	for _, topic := range topics {
		if len(out) >= max {
			break
		}
		off, err := b.store.Offset(ctx, pluginID, projectID, topic)
		if err != nil {
			return nil, err
		}
		latest, err := b.store.LatestID(ctx, topic, projectID)
		if err != nil {
			return nil, err
		}
		if latest-off > b.backlogCap {
			// Too far behind: dead-letter the overflow and jump the offset forward.
			newOff := latest - b.backlogCap
			if err := b.store.DeadLetter(ctx, pluginID, projectID, topic, off, newOff, "backlog_cap_exceeded"); err != nil {
				return nil, err
			}
			if err := b.store.SetOffset(ctx, pluginID, projectID, topic, newOff); err != nil {
				return nil, err
			}
			off = newOff
		}
		rows, err := b.store.After(ctx, topic, projectID, off, max-len(out))
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// Ack advances a subscriber's offset to upTo (monotonic). Events at or below upTo
// will not be re-delivered.
func (b *Bus) Ack(ctx context.Context, pluginID, projectID, topic string, upTo int64) error {
	cur, err := b.store.Offset(ctx, pluginID, projectID, topic)
	if err != nil {
		return err
	}
	if upTo <= cur {
		return nil
	}
	return b.store.SetOffset(ctx, pluginID, projectID, topic, upTo)
}
