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

### D2 — Globally-unique int64 ids over Streams; injective keys

The `int64` id is the at-least-once **idempotency key** (the SDK dedupes on it), so
it MUST be **globally unique** across all topics — matching Postgres's table-wide
`BIGSERIAL`. (A per-topic counter would restart the id at 1 per topic, so a
subscriber to two topics that dedupes on id alone would silently drop events on
scale but not lite — a two-profile divergence.) `Append` is a Lua script that
`INCR`s a **single global seq key** and `XADD`s the entry with the explicit id
`<n>-0`; ids are globally unique and strictly ascending within each per-(topic,
project) stream, so `After` is a plain `XRANGE (afterID-0 +` and `LatestID` is the
stream's last entry (`XREVRANGE`). `SetOffset` is a Lua script that advances only on
a greater id (the GREATEST monotonic guarantee the Postgres store enforces in SQL).

**Key injectivity (tenant isolation):** every key component — `project`, `topic`,
`pluginID`, and `topic` is attacker-controlled (a plugin's poll body) — is
percent-encoded so the `:` separator and Redis glob/hash-tag metacharacters can
never appear in a component. A raw `project|topic` concatenation would NOT be
injective (`acme`+`secret|audit` and `acme|secret`+`audit` collide), a cross-tenant
read/write break; encoding makes the mapping injective regardless of content. Both
the global-id uniqueness and the adversarial-identifier injectivity are conformance
cases run against every backend (D5).

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
conformance. H6 backlog-replay, at-least-once, backlog-cap→DLQ, tenant/topic
isolation, **global-id uniqueness across topics**, and **adversarial key
injectivity** all pass green over Valkey Streams. (The last two were added after the
adversarial review found the suite was blind to the per-topic-id and key-collision
divergences — every case a reviewer finds becomes a permanent fixture.)

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

## Scope: single-node / Sentinel, not Redis Cluster

The backend targets a single primary with Sentinel HA (`Dial` builds only
`NewClient`/`NewFailoverClient`, never `NewClusterClient`). The global seq counter
(for globally-unique ids) and the append/DLQ multi-key operations span different
hash slots, which would `CROSSSLOT`-fail under Redis Cluster. Cluster support (with
hash-slot-aware key co-location or a Cluster-safe id scheme) is a deliberate
follow-up, NOT a silent gap — a future "add cluster" change must revisit the id
counter and the two-key Lua/pipeline ops.

## Deferred / follow-up

- Stream retention/trimming (the log grows unbounded, matching the Postgres log;
  bounded by the backlog-cap semantics — anything older than a within-cap subscriber
  is DLQ territory). A `MAXLEN`-based trim to ~backlogCap is a memory follow-up.
- A low-latency wake (`SetNotifier` via Redis pub/sub) — the base poll model is
  sufficient and matches the Postgres reference; a blocking `XREAD` path would then
  rely on the R-EV2 keepalive.
