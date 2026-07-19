# Event schemas — v1alpha1

JSON Schemas for event bus payloads (D1 event bus, consumed via the SDK `events`
primitive). Events are how plugins react to data lifecycle without touching
infrastructure: e.g. "trace ingested", "score written". Consumer groups are
durable with a dead-letter path.

Each event payload has its own schema here. Event schemas follow the same
K8s-style maturity rules as the rest of `api/`: additive-only within a maturity
version; a breaking change means a new maturity version, not an in-place edit.

## Delivery contract — poll / ack / fail

Delivery is **at-least-once**: each event carries a monotonic `id` that is both the
replay offset and the idempotency key (dedupe on it). A subscriber holds a durable
per-`(plugin, project, topic)` offset and drives it with three operations:

- **`poll(topics, max)`** returns events after the offset **without** advancing it.
- **`ack(topic, upTo)`** advances the offset — those events are not re-delivered.
- **`fail(topic, id, reason)`** dead-letters exactly ONE event and advances past it.

`fail` exists so a subscriber can classify the **permanent-vs-transient** distinction
that at-least-once delivery otherwise cannot express:

| Failure kind | Subscriber action | Result |
|---|---|---|
| **Transient** (downstream momentarily down) | do NOT ack, do NOT fail | event is re-delivered on the next poll — retried, **never dropped** |
| **Permanent** (malformed subject, deterministic rejection) | `fail(topic, id, reason)` | event is dead-lettered and skipped; good events queued behind it flow |

Without `fail`, a permanently-failing event forces a subscriber to either loop on it
forever — pinning the log until the per-subscriber backlog cap bulk-drops the poison
**and** every good event behind it — or ack past it silently (data loss with no audit
row). `fail` is the explicit permanent path that avoids both.

**Head-only rule.** `id` must be the head of the subscriber's unacked window (the
lowest un-acked id). Failing an id further ahead would silently skip — and thereby ack
— the unprocessed events in between, so the kernel refuses it with `409`. Ack the good
events before the poison first, then fail the poison. Failing an already-passed id is an
idempotent no-op.

**The dead-lettered event is not destroyed.** `fail` records that this subscriber
skipped `id` and advances its offset; the event stays in the durable log, so its
payload remains readable for recovery and inspection. The recorded reason is prefixed
`subscriber:` to distinguish it from a kernel-generated reason (e.g.
`backlog_cap_exceeded`).
