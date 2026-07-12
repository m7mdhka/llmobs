# Scaling from lite to scale — the never-strand guarantee

> **The promise:** we never strand a self-hoster on a migration cliff. When you
> outgrow the lite (Postgres) profile and turn on the scale (ClickHouse) engine,
> your existing traces do not disappear, not even for a moment. This is not a
> best-effort claim — it is a **tested invariant** (ADR-0026, RULING-MIG6).

Every other open-source LLM-observability tool that added a scale engine made
migration a hard cutover: run a batch job, hope it finishes, and until it does
(or if it half-fails) your product looks empty. Langfuse revolted over this twice.
LLMObs takes a different stance: **dual-read is permanent and on by default; the
backfill is an optional convenience you can run whenever — or never.**

## How it works

### 1. Permanent dual-read (always on when scale is configured)

Set `LLMOBS_CLICKHOUSE_URL` and the kernel runs both engines behind one Query API:

- **Historical data** stays on Postgres-lite. Still readable, forever.
- **New data** is written to ClickHouse-scale.
- **Every read** — list, get-by-id, trace tree, aggregation, the implicit
  "do I have any data yet?" onboarding checks — resolves against the **union** of
  both engines. There is no read path that queries a single engine directly.

This works because lite already stores exactly one merged row per
`(project_id, id)` — the same logical contract scale reads — so the two can be
unified by a merge that dedups on that identity (preferring scale for a migrated
row). A span written to scale is immediately readable through the unified view,
and a historical span in lite is always readable, with **no window where either
is missing** — proven in both directions and under concurrent load
(`internal/storage/dualstore/dualread_test.go`).

**ClickHouse version requirement (GDPR erasure).** The kernel refuses to start against
a ClickHouse older than **23.8**. GDPR erasure uses a lightweight `DELETE`, and reads
rely on `apply_deleted_mask` to guarantee an erased span is *never* read back (even
before the physical merge); below 23.8 that guarantee is not reliable, so booting is a
fail-loud error rather than a silent risk of serving erased data. On versions that have
the lazy-materialization optimization, the kernel disables it on reads (feature-detected
at boot) so a query-plan reorder cannot surface an erased row past the mask.

```
LLMOBS_CLICKHOUSE_URL=clickhouse://user:pass@ch-host:9000/llmobs
# optional cluster name for ON CLUSTER DDL (empty = standalone; "default" is refused):
LLMOBS_CH_CLUSTER=my_cluster
# per-query resource caps (fail-closed defaults shown):
LLMOBS_CH_MAX_EXECUTION_TIME=30s
LLMOBS_CH_MAX_MEMORY_BYTES=2147483648
LLMOBS_CH_MAX_ROWS_TO_READ=50000000
LLMOBS_CH_MAX_BYTES_TO_READ=5368709120
```

**Query response-size ceiling (both profiles).** The Query API bounds the row
*count* of a page, but a page of wide-payload rows (large `input`/`output` blobs)
can still serialize into an enormous response. `LLMOBS_QUERY_MAX_RESPONSE_BYTES`
caps the serialized size of a single query or trace-tree response; a query that
would exceed it gets a typed `response_too_large` **413** telling the caller to
narrow the query or paginate — never an out-of-memory crash of the kernel. This
guard applies in **both** the lite (Postgres) and scale (ClickHouse) profiles,
enforced identically in each adapter (cross-adapter conformance covers it). Default
is 32 MiB; `0` disables the bound (not recommended).

```
LLMOBS_QUERY_MAX_RESPONSE_BYTES=33554432   # 32 MiB (default); both profiles
```

### Scale event bus (Redis/Valkey Streams)

**Managed-cloud auth (AWS ElastiCache IAM / GCP Memorystore).** Beyond a static
`LLMOBS_EVENT_REDIS_PASSWORD`, the bus supports an ACL/IAM **username** and a
**rotating token** read from a file on every (re)connect — so a sidecar can refresh a
short-lived credential (ElastiCache IAM tokens ~15 min) without restarting the kernel.
The password file takes precedence over the static password. A credential rejection is
classified **permanent** (it fails loud rather than retrying a doomed AUTH forever); a
network blip stays transient/retriable.

```
LLMOBS_EVENT_REDIS_USERNAME=my-iam-user       # ACL/IAM user (optional)
LLMOBS_EVENT_REDIS_PASSWORD_FILE=/var/run/redis/token  # rotating token; re-read each connect
```

**Durability + retention.** Run Valkey with AOF on and a **non-evicting** memory
policy so the unconsumed Streams backlog is never silently lost or evicted — a full
memory returns loud backpressure to the producer instead. The kernel bounds each
stream to the delivery window (`LLMOBS_EVENT_BACKLOG_CAP`) via an approximate trim, so
the log cannot grow without limit while the full deliverable window is always retained.
The bundled `deploy/compose/scale.yaml` sets `--appendonly yes --appendfsync everysec
--maxmemory-policy noeviction`; mirror it in any custom Valkey deployment.

A store that is 0%, 50%, or 100% migrated is **equally correct to a reader**. That
is the whole point: correctness never depends on the backfill having finished.

### 2. Optional resumable backfill (run it whenever you like)

The backfill copies cold historical rows from lite onto the scale engine, so that
over time the older data also benefits from ClickHouse. Because dual-read already
makes everything readable, this is pure housekeeping — **safe to start, stop,
restart, or skip entirely.**

```
LLMOBS_BACKFILL_ON_BOOT=true
LLMOBS_BACKFILL_CHUNK_SIZE=500      # rows per batch (bounded)
LLMOBS_BACKFILL_BUDGET=30m          # SEPARATE execution budget per run (see below)
```

It is built to survive the failure modes that broke other tools' migrations:

- **Total-ordered `(timestamp, project_id, id)` cursor.** Resumes exactly where it
  left off, and a cluster of rows sharing one timestamp never loops forever (the
  bug that stalled a well-known v4 backfill was a timestamp-only cursor).
- **Its own generous execution budget** — deliberately *not* the interactive query
  timeout. A short read timeout applied to a long migration is exactly what broke
  that same v4 backfill; here the budget is separate and configurable.
- **Bounded chunks, per-batch retry with backoff, visible progress** in the logs
  (`backfill progress kind=spans migrated=… cursor_ts=…`).
- **Fail loud, never silent-hang.** A persistent lite-read failure stops the run
  with an error you can see; a single malformed or deterministically-rejected row
  is dead-lettered (recorded in `backfill_deadletter`, auditable, re-drivable) so
  one bad row can never pin the whole migration.
- **Decoupled from boot readiness.** It runs in the background; `/readyz` never
  waits on it. A restart mid-run resumes from the last durable cursor.

The whole run is idempotent: replaying a settled row re-folds to itself, so
running the backfill twice (or from two replicas) never corrupts data.

## What you should expect operationally

| Moment | What a reader sees |
| --- | --- |
| Before you set `CLICKHOUSE_URL` | Everything, from lite. |
| The instant you enable dual-read | Everything — old from lite, new from scale, unified. |
| While the backfill runs | Everything, unchanged. Progress in logs. |
| After the backfill completes | Everything — now served increasingly from scale. |
| If the backfill errors or you stop it | Everything, still. Re-run when ready. |

There is no step in that table where data is missing. That is the guarantee.

## Proof

This page is backed by tests, not assertions:

- `internal/storage/dualstore/dualread_test.go` — read-after-write across the
  boundary, both directions, under concurrent load.
- `internal/dataplane/query/dualseam_test.go` — every read handler resolves
  through the unified dual seam (no single-adapter read path exists).
- `internal/storage/backfill/backfill_test.go` — cursor total-order (no
  same-timestamp loop), resumability, dead-lettering, transient retry.
- `internal/storage/backfill/integration_test.go` —
  `TestNeverStrandDualReadThenBackfill`: disjoint lite/scale data is fully visible
  before any backfill, and stays visible through it, against both real engines.

See [ADR-0026](../adr/0026-scale-profile-clickhouse-adapter.md) (RULING-MIG6) for
the decision record.
