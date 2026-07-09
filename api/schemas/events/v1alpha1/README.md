# Event schemas — v1alpha1

JSON Schemas for event bus payloads (D1 event bus, consumed via the SDK `events`
primitive). Events are how plugins react to data lifecycle without touching
infrastructure: e.g. "trace ingested", "score written". Consumer groups are
durable with a dead-letter path.

Each event payload has its own schema here. Event schemas follow the same
K8s-style maturity rules as the rest of `api/`: additive-only within a maturity
version; a breaking change means a new maturity version, not an in-place edit.
