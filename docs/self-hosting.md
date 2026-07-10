# Self-hosting notes

Interim guidance for deployment shapes the flexibility audit surfaced. These are
supported patterns today; the deeper integrations they gesture at (OIDC, Tier-3
processors) are tracked separately.

## Headless (no web shell)

The kernel serves the web shell at `/` by default. To run it as a pure backend —
your own UI calls the Query API with a server-side credential (Kenji, Story 10) —
set:

```
LLMOBS_SERVE_SHELL=false
```

With the shell off, `/` is API-only; the OpenAPI at `api/openapi/v1alpha1` and a
machine API key (below) are all a non-browser client needs. This is also the
right mode for a terminal/TUI client (Omar, Story 19).

### Machine credentials

Browser callers authenticate with a session cookie; machines use a **bearer API
key**. An admin issues a scoped key once:

```
POST /v1alpha1/api-keys        (admin session + CSRF)   → { "secret": "sk-…", "public_key": "pk-…", "scopes": [...] }
GET  /v1alpha1/api-keys                                 → { "keys": [ { public_key, scopes, created_at } ] }
DELETE /v1alpha1/api-keys/{public_key}                  → revoke
```

Scopes: `ingest`, `query`, `scores:write`, `delete`. The secret is shown once.
Use it as `Authorization: Bearer sk-…` on the Query/ingest APIs.

## Custom redaction (interim: a proxy in front)

Server-side redaction-before-persistence is a real seam (`pipeline.redactStage`)
but is not yet third-party-injectable (issue #11). Until a processor-injection
contract lands, the supported pattern for PHI/PII scrubbing (Dr. Chen, Story 4)
is a **redaction proxy in front of the OTLP endpoint**:

```
services ──OTLP──▶ [ your redaction proxy ] ──OTLP──▶ LLMObs kernel (:4318 / :4317)
```

The proxy receives OTLP, scrubs prompt/completion payloads (your in-house NER
model or rules), and forwards cleaned OTLP to the kernel. Because it sits *before*
ingestion, no unredacted payload is ever persisted, and you keep it server-side
without changing every emitting service. This is a deployment component you own;
the kernel needs no changes.

## The OTel Collector in front (first-class topology)

Most production estates route telemetry through a fleet of OpenTelemetry
Collectors (tail-sampling, attribute-scrubbing, batching, multi-exporter fan-out).
LLMObs is **an OTLP exporter target** — point a Collector's `otlp` exporter at the
kernel's `:4317`/`:4318`:

```yaml
exporters:
  otlp/llmobs:
    endpoint: llmobs-kernel:4317
    headers: { authorization: "Bearer ${LLMOBS_API_KEY}" }
service:
  pipelines:
    traces: { receivers: [otlp], processors: [tail_sampling, batch], exporters: [otlp/llmobs] }
```

**What to expect when the Collector mangles reality:**
- **Tail-sampling drops spans**, so a trace can arrive missing its root or middle
  spans. The kernel detects this: a span referencing a parent absent from the
  trace stamps **`llmobs.dq.incomplete_trace`**, exposed on the synthesized trace
  and **filterable** (`{"field":"incomplete_trace","op":"eq","value":true}`). The
  tracing UI badges incomplete traces.
- **Trace-level rollups (cost, root name) are best-effort** on incomplete traces —
  treat `incomplete_trace=true` rollups as lower bounds.
- **Scrubbed/rewritten attributes** are preserved as-is; the kernel never assumes
  a resource attribute is present.
- **`/metrics`** exposes ingest rate and error-span rate so you can reconcile
  against the Collector's own sampled counts.

Fixtures under `kernel/testdata/fixtures/otel-genai/collector-*` represent
Collector-mangled inputs and run through conformance, so this topology is a
tested first-class citizen — not an afterthought.

## Multi-tenant emitter (router-shim pattern)

If one process (e.g. a company-wide LiteLLM proxy) emits traffic for many
projects, the kernel binds **one API key to one project** at the connection level
(tenant isolation). Per-span project routing is not a kernel feature today
(tracked: `ingest:route`). The honest v1 is a **single router shim** in front:

```
LiteLLM proxy ──OTLP──▶ [ router: reads team_id, forwards with that project's key ] ──▶ kernel
```

One process, not forty exporters. The shim owns the `team_id → (project, key)`
mapping; the kernel sees clean, correctly-attributed streams per project.

## Bring-your-own transport (Kafka→OTLP bridge)

If your estate forbids service-to-service HTTP and mandates Kafka, run a **bridge
consumer** that reads your OTLP-carrying topic and forwards to the kernel's OTLP
endpoint:

```
services ──▶ Kafka (OTLP/Avro) ──▶ [ bridge consumer ] ──OTLP──▶ kernel
```

Redelivery is safe: the merge is idempotent on `(project_id, id)` with a
producer-derived `event_ts`, so a re-consumed span folds to the identical state —
**exactly-once-ish by construction**, no dedup store needed. A first-party Kafka
*receiver* inside the kernel is a possible future addition (the receiver→pipeline
seam is clean); until then the bridge pattern needs no kernel changes.

## Durability & shutdown (the async-ack promise)

Ingestion is **async-ack**: the OTLP receiver reads the request, enqueues it in an
in-process buffer, and returns `200/OK` — the database write happens on a worker a
moment later. This keeps ack latency off the database, but it means an acked span
lives only in memory until it is persisted. The honest question is: *is that ack a
promise we can keep?*

- **Rolling deploys / SIGTERM: the queue is drained, not dropped.** On shutdown the
  receivers immediately shed new requests with a retryable `503`/`UNAVAILABLE`,
  stop the listeners, then drain the in-flight queue to Postgres before exiting.
  A normal deploy loses nothing.
- **The one residual loss window is bounded and counted.** If a forced termination
  outlives the drain deadline (`LLMOBS_SHUTDOWN_DRAIN_TIMEOUT`, default `20s`), the
  jobs still queued at that instant are the unavoidable floor of an in-memory
  queue. They are **counted** (`llmobs_ingest_queue_dropped_on_shutdown_total`) and
  logged at error — observable, never silent.
- **Set the grace period accordingly.** `LLMOBS_SHUTDOWN_DRAIN_TIMEOUT` must be
  shorter than your orchestrator's `terminationGracePeriodSeconds` (K8s default
  `30s`) with headroom for the ~5s server shutdown; the `20s` default fits the
  default grace comfortably. Raising the drain timeout shrinks the loss window at
  the cost of a slower shutdown.
- **Crash (SIGKILL/OOM) is the same floor, uncounted.** A hard kill cannot drain;
  recovery is client retry into the idempotent merge (redelivery folds to the same
  state). The **lite** profile accepts this bounded window by design; the **scale**
  profile's durable ingest path (S3/WAL spool) closes it entirely and is tracked
  separately — lite makes the window honest and bounded, it does not eliminate it.

### Backpressure when persistence is failing

If Postgres becomes unwritable (disk full, failover) the kernel must not keep
acking `200` into a queue that cannot drain — that would grow the loss window
without bound. Instead:

- **Persist health feeds readiness.** A run of persist failures
  (`LLMOBS_PERSIST_UNHEALTHY_THRESHOLD`, default `5`) flips `/readyz` to not-ready.
  A connectable-but-unwritable database still answers a ping, so readiness checks
  the *write* signal, not just connectivity.
- **The receivers shed, not lie.** While unhealthy — or when the queue passes its
  high-water mark (80% of capacity) — the OTLP endpoints return a retryable `503`
  with `Retry-After` (HTTP) / `UNAVAILABLE` (gRPC). Clients back off and retry into
  the idempotent merge; nobody gets a `200` for a span that won't be stored.
- **Diagnose from `/metrics`.** `llmobs_persist_healthy`, `llmobs_ingest_queue_depth`
  / `_capacity`, and `llmobs_ingest_backpressure_shed_total{reason=…}` (plus the
  per-project `llmobs_ingest_spans_total`) show saturation and the noisy-tenant
  case building before it bites.
- **Single-replica is honest backpressure, not a bug.** With one replica there is
  nowhere to shed to, so it reports not-ready and rejects new ingest while
  persistence is down. That is correct: clients retry, no data is lost to a false
  ack. It is also the signal to run more replicas or move to the scale profile.

## GDPR erasure

Delete every span for a user, provably:

```
DELETE /v1alpha1/spans?user_id=<id>&from=<rfc3339>&to=<rfc3339>   (delete scope)
     → { "erased": <count>, "audit_id": "era_…" }
```

The filter is mandatory and bounded (a user plus a time window); the deletion is a
hard delete (payloads removed, not merely hidden) and records an `erasure_audit`
row (actor, filter, count, time) so the erasure is auditable.
