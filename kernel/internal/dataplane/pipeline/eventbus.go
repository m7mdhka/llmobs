package pipeline

import "context"

// EventBus is the publish interface (SDK `events` primitive substrate). The lite
// profile ships an in-proc no-op; durable Redis Streams / NATS come with the
// plugin system.
// TODO(issue): durable event bus (Redis Streams lite / NATS scale).
type EventBus interface {
	Publish(ctx context.Context, topic, key string) error
}

// NoopBus discards published events.
type NoopBus struct{}

func (NoopBus) Publish(context.Context, string, string) error { return nil }
