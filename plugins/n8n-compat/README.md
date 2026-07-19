# n8n workflow tracing (compat plugin)

Traces [n8n](https://n8n.io) workflow runs in LLMObs with **no upstream
instrumentation** — the #1 community request an incumbent
[structurally cannot ship](../../docs/positioning.md#pillar-4--kernel-plus-plugins-vs-monolith-economics),
delivered as a container + a manifest.

## How it works

n8n emits no OpenTelemetry, so there is no wire dialect to normalize on the ingest hot
path. This is a **cold-path compat plugin**: on a schedule the kernel triggers a poll,
the plugin fetches finished executions from n8n's REST API, maps each run into canonical
spans, and pushes them through the `ingest` capability.

The mapping (`backend/translate.py`, the fixture-tested core):

- a workflow execution becomes a **trace**; its root span is the workflow;
- each node run becomes a **child span** — the node graph (via each run's `source`
  → `previousNode`) becomes the span tree;
- node timing (`executionTime`), error status, and the workflow status are preserved;
  n8n ids are hashed to OTLP ids with the originals kept under `n8n.*` attributes.

## Operator setup

1. Create an n8n API key (n8n → Settings → API) and store it in the plugin's `secrets`
   under `n8n_api_key`.
2. Set `N8N_API_URL` to your n8n base URL (e.g. `https://n8n.example.com`).
3. Install the plugin; the kernel polls every minute (manifest `jobs` schedule) and
   ingests new executions. The poll cursor (last-seen execution id) is kept in `kv`, so a
   restart resumes without duplicates (ingest is idempotent on the derived span ids).

## Failure handling and a known limitation

- **Permanent vs transient.** An execution that can never translate or that the kernel
  rejects as malformed (a 4xx) is **dead-lettered** — skipped, with the cursor advanced —
  so one bad execution can never block every newer one. A transient failure (n8n or the
  kernel briefly down, a 5xx) aborts the cycle without advancing, so it is retried and
  never dropped.
- **Completion gate (a deliberate tradeoff).** The cursor is a single execution id, and
  the poller stops at the first still-running execution rather than leapfrogging it — so
  no run is ever skipped. The cost is latency: a long-running workflow delays tracing of
  newer executions until it finishes. This favors completeness over latency; a production
  poller that needs both would use a completion-time window with overlap instead of a
  strict id cursor.

## Capabilities requested

`ingest` (push spans), `secrets` (the n8n API key), `kv` (the poll cursor). Backend-only;
no frontend surface. Structured exactly like a third-party plugin — nothing here imports
kernel internals.
