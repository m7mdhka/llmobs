"""n8n-compat backend — traces n8n workflows with no upstream instrumentation.

n8n emits no OpenTelemetry, so there is no wire dialect to normalize on the hot path.
This cold-path plugin POLLS n8n's execution REST API on a schedule, maps each finished
workflow run into canonical spans (translate.py), and pushes them through the kernel
`ingest` capability. The whole contract is spoken over HTTP via the public protocol — no
kernel internals.

Poll cycle (driven by the kernel scheduler POSTing to /plugin/v1/poll with a system
identity assertion scoped to this plugin's grant):
  1. read the n8n API key from `secrets` and the last-seen execution id from `kv`;
  2. list finished executions newer than the cursor (oldest-first so the cursor advances
     monotonically and a crash mid-cycle only re-processes — ingest is idempotent);
  3. fetch each execution's run data, translate to OTLP, ingest;
  4. advance the `kv` cursor and the health watermark.

Config (env):
  LLMOBS_KERNEL_URL   kernel base URL (e.g. http://kernel:8080)
  N8N_API_URL         the n8n instance base URL (operator-configured)
"""

from __future__ import annotations

import os
import time

from fastapi import FastAPI, Request, Response

from llmobs_plugin import IDENTITY_ASSERTION_HEADER, KernelClient
from poll import run_poll_cycle

PLUGIN_ID = "llmobs/n8n-compat"

app = FastAPI()

# The service token is DELIVERED by the kernel at handshake (there is no pull path); seed
# from env only as a local-dev fallback.
_token = {"value": os.environ.get("LLMOBS_SERVICE_TOKEN", "")}
_last_progress = {"unix": int(time.time()), "detail": "startup"}


@app.post("/plugin/v1/token")
async def receive_token(request: Request):
    body = await request.json()
    _token["value"] = body.get("serviceToken", "")
    return Response(status_code=200)


@app.get("/plugin/v1/info")
def info():
    return {
        "id": PLUGIN_ID,
        "version": "0.1.0",
        "pluginApiVersion": "v1alpha1",
        # MUST be a subset of the manifest's granted capabilities (the supervisor rejects
        # over-reach).
        "capabilities": ["ingest", "secrets", "kv"],
        "displayName": "n8n workflow tracing",
    }


@app.get("/plugin/v1/health")
def health():
    return {
        "live": True,
        "ready": bool(os.environ.get("LLMOBS_KERNEL_URL") and os.environ.get("N8N_API_URL")),
        "watermark": {"lastProgressUnix": _last_progress["unix"], "detail": _last_progress["detail"]},
    }


def _kernel() -> KernelClient:
    return KernelClient(os.environ["LLMOBS_KERNEL_URL"], _token["value"])


@app.post("/plugin/v1/poll")
async def poll(request: Request):
    """Kernel-scheduled poll trigger. The kernel POSTs here on the manifest cadence with a
    system identity assertion scoped to this plugin's grant (needed for kv + secrets)."""
    assertion = request.headers.get(IDENTITY_ASSERTION_HEADER, "")
    if not assertion:
        return Response(status_code=401, content="missing identity assertion")
    try:
        summary = run_poll_cycle(_kernel(), assertion)
    except Exception:  # a whole-cycle failure (e.g. n8n unreachable) is transient; retry
        # Generic body — never echo the exception (defence-in-depth against leaking an
        # upstream response into the reply). The type is logged server-side.
        return Response(status_code=503, content="poll cycle failed")
    if summary["processed"] or summary["skipped"]:
        _last_progress["unix"] = int(time.time())
        _last_progress["detail"] = summary["detail"]
    return summary
