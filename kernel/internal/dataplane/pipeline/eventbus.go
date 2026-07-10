package pipeline

import "context"

// EventBus is the publish interface (SDK `events` primitive substrate). The lite
// profile ships a durable Postgres-backed bus (internal/bus); Redis Streams is the
// deferred scale backend behind this same interface. subjectID is the id of the
// entity the event is about (span/trace/score), so a subscriber can fetch it via
// the query primitive; projectID scopes delivery to the tenant.
type EventBus interface {
	Publish(ctx context.Context, topic, projectID, subjectID string) error
}

// NoopBus discards published events (used where no bus is wired, e.g. unit tests).
type NoopBus struct{}

func (NoopBus) Publish(context.Context, string, string, string) error { return nil }
