# ADR-0036: Event bus poison-message dead-letter (the permanent-vs-transient seam)

- **Status:** Accepted
- **Date:** 2026-07-19
- **Deciders:** m7mdhka
- **Relates to:** ADR-0023 (plugin protocol — the `events` primitive this extends),
  ADR-0028 (the Redis/Valkey scale bus backend), ADR-0027 (durable ingest — the same
  permanent-vs-transient failure taxonomy). Evidence: the Opik cross-over mine
  (`comet-ml/opik` retry-forever failure class) and the project invariant that every
  retry/requeue path needs an explicit permanent-vs-transient taxonomy.

## Context

The event bus delivers at-least-once: a subscriber holds a durable per-`(plugin,
project, topic)` offset, `poll` returns events after it without advancing, and `ack`
advances it. The only dead-letter path was `backlog_cap_exceeded` in `Poll` — a
*slow-subscriber* guard that fires when a subscriber falls more than the per-subscriber
backlog cap behind the latest event.

There was no path for a **poison message**: a single event whose handler fails
permanently (a malformed subject, a deterministic plugin bug on that one record). With
only poll + ack, such a subscriber has two bad options, both of which the project's
failure-taxonomy invariant forbids:

1. **Never ack past it.** The offset never advances, so the poison is re-polled forever
   (head-of-line blocking — every good event behind it is starved). Meanwhile the log
   grows until `latest - offset > backlogCap`, at which point `Poll` bulk-dead-letters
   the *entire* overflow range — the poison **and** all the good events queued behind it
   — under the misleading reason `backlog_cap_exceeded`. One poison record silently
   discards a large window of good data.
2. **Ack past it silently.** Data loss with no audit row, no counter, no reason — the
   exact "drop to make room" failure the invariant warns against.

The failure path, not the happy path, was hiding the worst bug.

## Decision

Add a third operation to the events primitive — **`fail(topic, id, reason)`** — the
explicit **permanent-failure classifier at the seam**. The taxonomy becomes:

- **Transient failure** (the subscriber's downstream is momentarily down): the
  subscriber does NOT ack and does NOT fail. The event is re-delivered on the next poll
  — retried, **never dropped**.
- **Permanent failure** (malformed subject, deterministic rejection): the subscriber
  calls `fail`. The event is dead-lettered and the offset advances past it, so the good
  events queued behind the poison flow on the next poll.

The subscriber is the only party that can distinguish these — a kernel-side
delivery-attempt counter that auto-dead-letters after N tries *cannot*, because it can't
tell a permanent poison from a subscriber that was simply down for N polls, so it would
drop transient failures (violating the invariant). Classification therefore lives with
the subscriber; the kernel provides the mechanism and the durable audit.

### Design specifics

- **Head-only.** `fail`'s `id` must equal `offset + 1` (the head of the unacked
  window). Failing a later id would silently skip — and thereby ack — the unprocessed
  events in between, so it is refused (`ErrNotHead` → HTTP `409`). Ack the good events
  before the poison first, then fail the poison. `id <= offset` is an idempotent no-op
  (a retried fail after the offset already moved).
- **No new Store method.** `fail` composes the existing `DeadLetter(from, to, reason)`
  and `SetOffset` at the backend-agnostic `Bus` level — exactly as the `backlog_cap`
  path already does — so no bus backend (in-memory, Postgres, Redis/Valkey) changes and
  the DLQ schema is untouched. The conformance suite proves the semantics against every
  backend.
- **The event is not destroyed.** `fail` records that this subscriber skipped `id` and
  advances its offset; the event stays in the durable log, so its payload is recoverable
  and inspectable. This is a per-subscriber skip record, not a delete.
- **Reason is sanitized.** The subscriber-supplied reason is stripped of control
  characters, length-bounded, and prefixed `subscriber:` so it can never be confused
  with a kernel-generated reason (e.g. `backlog_cap_exceeded`).
- **`backlog_cap_exceeded` stays** as the last-resort slow-subscriber guard. With `fail`
  available, a well-behaved subscriber skips a poison immediately and never accumulates
  backlog from it, so a single poison no longer rides the log to the bulk-drop.

### SDK ergonomics

The `subscribe()` convenience loop maps the taxonomy onto handler outcomes: a normal
return acks; throwing `PermanentEventError` fails-and-skips; any other throw is treated
as transient (the batch stops at that event and it is re-delivered). Ack/fail happen per
event, so a permanent poison never blocks the good events behind it.

## Consequences

- The `events` contract gains an additive `fail` operation within `v1alpha1` (no break).
- A poison message can no longer head-of-line-block a topic or trigger the bulk-drop of
  good events; permanent failures are audited, transient failures are never dropped.
- Subscribers must opt into `fail` for permanent failures; a subscriber that ignores it
  behaves exactly as before (and can still, as a last resort, hit `backlog_cap`).

## Alternatives considered

- **Kernel-side auto-dead-letter after N delivery attempts.** Rejected: the kernel
  cannot distinguish a permanent poison from a subscriber that was down for N polls, so
  it would drop transient failures — the precise anti-pattern the invariant forbids.
- **A range `fail(upTo)` mirroring `ack(upTo)`.** Rejected: it conflates "these are all
  poison" with the real case (one poison among good events) and would dead-letter good
  events. Single-event, head-only fail models the actual failure precisely.
- **A new `Store.FailOne` method for atomicity.** Deferred: the `Bus`-level composition
  of `DeadLetter` + `SetOffset` matches the existing `backlog_cap` path and is
  idempotent on retry (a crash between the two re-fails the same head on the next
  attempt, adding at most a duplicate skip record — the DLQ is at-least-once, like
  delivery). Not worth a three-backend interface change.
