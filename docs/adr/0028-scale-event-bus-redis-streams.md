# ADR-0028: Scale event bus — Redis/Valkey Streams backend

- **Status:** Accepted (Arc L / L4 — the scale event backend)
- **Date:** 2026-07-11
- **Deciders:** m7mdhka (Arc L)
- **Relates to:** ADR-0023 (plugin protocol — the `events` primitive this
  substantiates), ADR-0026 (the lite/scale Store-seam pattern this mirrors for the
  bus), ADR-0027 (durability/at-least-once, adjacent). Evidence: the Langfuse
  scale-issue mine (Redis Sentinel failover, idle-keepalive, Valkey/Dragonfly
  compat requirements R-EV1..R-EV3).

## Context

The event bus is factored exactly like storage: a shared, backend-agnostic
`bus.Bus` over a tiny 6-method `bus.Store` seam. The Bus owns ALL the hard logic —
replay-from-offset, at-least-once (poll-without-advance), per-subscriber offsets,
the per-subscriber backlog cap (10000) → dead-letter, tenant/topic isolation. A
backend implements ONLY persistence. The lite profile ships a Postgres-backed
`Store`; L4 adds the **scale** backend on Redis/Valkey Streams — parallel to the
Postgres-lite / ClickHouse-scale storage split.

## Decision

### D1 — Implement only `bus.Store` (6 methods); the Bus is untouched

`redisstore.Store` satisfies `bus.Store` (Append/LatestID/After/Offset/SetOffset/
DeadLetter). Replay/at-least-once/backlog-cap/DLQ stay in `bus.Bus` and are NOT
reimplemented. The **same conformance suite** proves it (D5).

### D2 — int64 monotonic offsets over Streams via an INCR-driven entry id

The Store contract is `int64` monotonic offsets per `(topic, project)` — the
idempotency key and the `After(afterID)` sort key. Redis Stream ids are `ms-seq`
strings, so the backend drives its own int64 via an `INCR` counter and writes each
entry with the **explicit id `<n>-0`**. The counter is both `LatestID` and the
entry's sort key, so `After` is a plain `XRANGE (afterID-0 +`. `Append` is a Lua
script (`INCR` + `XADD` atomic) so ids are strictly monotonic and the stream order
can never diverge from the counter under concurrent producers. `SetOffset` is a Lua
script that advances only on a greater id (the GREATEST monotonic guarantee the
Postgres store enforces in SQL).

### D3 — Sentinel HA failover, self-healing without a restart (R-EV1)

`Dial` builds a go-redis **Failover client** from `MasterName` + `SentinelAddrs`:
it resolves the current master through Sentinel and **re-resolves on failover**, so
a master promotion self-heals WITHOUT a process restart. Auth is carried to both the
data nodes (`Password`) and the Sentinels (`SentinelPassword`); retries are bounded
exponential (`MaxRetries=5`, 8ms..512ms backoff) — the transient-vs-permanent
taxonomy (invariant #12) at the connection layer. Proven by a real failover test
(force `SENTINEL FAILOVER` → the same client writes + reads on the promoted master,
with the pre-failover event replicated and present).

### D4 — Idle keepalive + Valkey/Dragonfly compat (R-EV2, R-EV3)

- **R-EV2:** the Store is **poll-based** (`XRANGE`, request/response), so it has no
  long-lived blocking read that could be mistaken for a dead socket. Idle pooled
  connections are health-checked (`ConnMaxIdleTime=30s`) and transparently redialed;
  go-redis handles a mid-flight broken connection as a transient retry.
- **R-EV3:** compat is proven by running the conformance GREEN against a real
  **Valkey 8.x** (not just Redis). `DisableIndentity=true` skips the `CLIENT
  SETINFO` identity handshake that some Valkey/Dragonfly/KeyDB versions reject — a
  feature-detect opt-out, NOT a hard Redis-version gate, applied on both producer
  and consumer connections. Boot fail-closes on an unreachable endpoint via `PING`.

### D5 — One conformance suite, every backend (the cross-backend proof)

The bus conformance moves to `internal/bus/bustest.RunConformance(t, factory)` and
runs against BOTH the in-memory backend and the real Redis/Valkey backend from the
identical assertions — the event-bus analogue of the cross-adapter storage
conformance. H6 backlog-replay, at-least-once, backlog-cap→DLQ, and tenant/topic
isolation all pass green over Valkey Streams.

### D6 — Selection + dependency

Config `EventRedisURL` or `EventRedisSentinelAddrs` selects the scale backend at
the single `bus.New(...)` call site; empty = Postgres-lite. Boot **fails closed** if
the configured Redis/Valkey can't init. New dependency: `github.com/redis/go-redis/
v9` (BSD-2, license-clean), pinned at a Go-1.24-compatible version so no toolchain
bump rides in (as with clickhouse-go in L1).

## Consequences

- A new `internal/bus/redisstore` package (kept out of the core `bus` package so
  lite does not pull the redis client) + `internal/bus/bustest` shared suite.
- The Postgres event schema (`0012_event_bus.sql`) is unchanged; the two backends
  are behaviorally interchangeable behind the Store seam.
- CLAUDE.md's stale "Redis Streams (lite bus)" line is corrected: **Postgres is the
  lite bus, Redis/Valkey Streams is the scale bus** (NATS JetStream is not used).

## Deferred / follow-up

- Stream retention/trimming (the log grows unbounded, matching the Postgres log;
  bounded by the backlog-cap semantics — anything older than a within-cap subscriber
  is DLQ territory). A `MAXLEN`-based trim to ~backlogCap is a memory follow-up.
- A low-latency wake (`SetNotifier` via Redis pub/sub) — the base poll model is
  sufficient and matches the Postgres reference; a blocking `XREAD` path would then
  rely on the R-EV2 keepalive.
