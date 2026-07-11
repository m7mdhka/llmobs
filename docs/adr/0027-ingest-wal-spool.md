# ADR-0027: Durable ingest spool — local WAL floor + async object-store tier

- **Status:** Accepted (Arc L / L3 — the durable ingest spool)
- **Date:** 2026-07-11
- **Deciders:** m7mdhka (Arc L, RULING-SP7)
- **Relates to:** ADR-0026 (scale profile; records RULING-SP7), ADR-0022 (time
  authority — the idempotent-merge property replay relies on), ADR-0016/0021
  (merge-on-write: re-delivery is idempotent). Supersedes the "in-memory queue"
  durability note in `internal/dataplane/ingest` for the scale profile.

## Context

The lite ingest path is async-ack: the receiver reads the body, enqueues it in a
bounded in-process channel (`chan job`, cap 4096), and acks — the full pipeline
(authenticate → normalize → redact → persist) runs on a worker pool AFTER the ack.
That buys a DB-free ack budget but leaves **two silent-loss windows**: (1) a SIGKILL
that outlives the G1 drain deadline loses everything still queued; (2) a persist
error drops the already-acked job (Warn-only, no retry). Arc-G counts (1) and never
claimed (2) was covered.

RULING-SP7 closes both with a durable spool, and rules the tiering:

> **Local WAL for the durable floor, async object storage as the archival/replay
> tier behind it — NOT object-storage-on-the-hot-path.** The ack becomes durable
> when bytes hit a local WAL (fast, no hot-path network, closes the SIGKILL window);
> a background flusher moves records to object storage as the durable archival +
> replay source. Preserve the p99 floor and all three guarantees (G1 drain, G2
> backpressure-via-spool-fill with the same 503-then-retry contract, G3
> tombstone-on-replay).

## Decision

### D1 — A `Spool` seam replaces the raw `chan job`

The ack-window becomes an interface: `Append` (durable enqueue), `Next` (worker
lease), `Commit` (mark persisted), `Len`/`Cap` (backpressure + gauges). Two impls:
- **`memSpool`** — the current bounded channel; lite default, behaviour unchanged.
- **`walSpool`** — the durable floor (below). Selected by config in the scale
  profile.

The receiver depends only on `Spool`; backpressure, drain, and gauges move behind
it. A new caller inherits durability by construction.

### D2 — WAL is the durable floor; ack is ack-after-durable

`walSpool.Append` encodes the job (`body`, `contentType`, `bearer`, `receivedAt` —
already a DB-free byte record), appends it to an append-only segment with a
per-record CRC and a monotonic `seq`, and **fsyncs before returning** — so the
receiver's ack is only sent once the bytes are durable. A torn tail (partial final
write) is detected by the CRC/length on replay and truncated.

### D3 — Idempotent replay against a checkpoint watermark

Because merge-on-write is idempotent (ADR-0016/0022: re-delivering a record folds to
the same state), the spool does NOT need exact per-record commit tracking. A
**checkpoint watermark** records the highest `seq` whose record is durably persisted
(the contiguous persisted prefix). On boot, every record with `seq > watermark`
replays through the pipeline — duplicates are harmless. This makes the watermark free
to lag the persisted frontier without correctness risk, and turns a crash into a
replay, not a loss. Segments fully below the watermark are truncated.

### D4 — G3 (erasure) is honored on replay BY CONSTRUCTION

Replay runs the SAME persist path (`store.PersistSpan`), whose erasure-suppression
guard evaluates the tombstone against **server time** (ADR-0026 / the L1 fix), not
any record-carried timestamp. So a replayed (or forged-and-replayed) erased span is
refused by the store exactly as a live re-delivery is — the WAL cannot bypass
suppression, because it replays *into* the guard, never around it. This is the
load-bearing invariant and is proven with a forged-replay-after-erase test
(erase → crash before checkpoint → replay → assert no resurrection).

### D5 — Async object-store tier behind the WAL (never on the hot path)

An `ArchiveSink` (object storage) receives **sealed** (rotated) WAL segments from a
background uploader — never the hot Append path. It is the archival + disaster-replay
source: if the local WAL is lost (disk failure, fresh node), boot restores segments
above the watermark from the sink. The hot path touches only the local WAL; the sink
is best-effort and lags. Interface-first so lite can use a no-op sink and scale an
S3/MinIO sink.

### D6 — Poison-record bound

A record that fails persist for non-transient reasons (malformed body) would replay
forever. After a bounded retry count it is moved to a DLQ segment and counted
(`llmobs_ingest_wal_dead_lettered_total`), never blocking the watermark — mirroring
the event bus's DLQ. Transient persist failures (DB down) are governed by G2
(health-driven shedding) and simply retried on the next drain/boot.

## Guarantees after L3 (scale profile, walSpool)

- **G1 drain** — undrained-at-deadline records are already durable; they replay on
  next boot instead of being dropped. `llmobs_ingest_queue_dropped_on_shutdown_total`
  → 0 for the WAL spool.
- **G2 backpressure** — spool-fill (bounded in-flight + WAL write failure/disk
  pressure) sheds with the SAME retryable `503` + `Retry-After: 1` (gRPC
  `UNAVAILABLE`); the client retries into the idempotent merge.
- **G3 tombstone-on-replay** — replay honors erasure suppression at the store guard
  (D4).
- **Ack-after-durable** — the SIGKILL window (loss #1) and the persist-error window
  (loss #2) are both closed: an acked record is on disk and replays until persisted.

## Security — the WAL is a pre-redaction at-rest surface

The spool sits BEFORE the pipeline's redaction stage (redaction runs on the worker,
after Append), so the WAL stores **raw OTLP bodies (unredacted prompts/completions
= PII) and the request bearer token**. This is inherent to replay — the record must
reproduce the exact request. Consequences and mitigations:

- WAL files are written **owner-only** (dir `0700`, segments/checkpoint `0600`).
- The WAL directory MUST be treated with the **same protection as the database**
  (disk encryption at rest, restricted host/volume access). A WAL leak exposes both
  PII and valid bearer tokens (which would allow request replay-auth).
- The WAL is **transient**: records are truncated after persist + checkpoint, so a
  span's raw body normally lives in the WAL only until it is persisted. A GDPR
  erasure targets the store; it does not scrub the WAL, but an erased span's WAL
  record is virtually always already truncated (it was persisted long before the
  erasure). The residual case — a not-yet-persisted span erased while still in the
  WAL — replays, is suppressed by the store (D4, no resurrection), and is truncated
  on the next checkpoint. Operators who need hard on-erase WAL scrubbing must shorten
  the checkpoint interval / segment size.

## Consequences

- New hot-path dependency: a local WAL write+fsync on Append (bounded, no network).
  fsync-per-append is the correctness floor; group-commit is a noted perf follow-up.
- A new `ArchiveSink` dependency (object storage) for the scale profile, off the hot
  path, license-clean.
- Lite is unchanged (memSpool default); the two profiles share one receiver.

## Deferred / follow-up

- Group-commit fsync batching (throughput; correctness unaffected).
- The real S3/MinIO `ArchiveSink` + kind e2e restore-from-object-store drill (the
  in-repo proof uses a fake sink; the interface + uploader/restore logic are tested).
