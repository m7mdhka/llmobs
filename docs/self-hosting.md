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

## GDPR erasure

Delete every span for a user, provably:

```
DELETE /v1alpha1/spans?user_id=<id>&from=<rfc3339>&to=<rfc3339>   (delete scope)
     → { "erased": <count>, "audit_id": "era_…" }
```

The filter is mandatory and bounded (a user plus a time window); the deletion is a
hard delete (payloads removed, not merely hidden) and records an `erasure_audit`
row (actor, filter, count, time) so the erasure is auditable.
