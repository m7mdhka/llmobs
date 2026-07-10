"""langfuse-compat backend — a Python plugin proving the language-agnostic claim.

An UNMODIFIED Langfuse SDK client points LANGFUSE_HOST at this service; it accepts
Langfuse-wire ingestion, translates to OTLP (translate.py), and pushes through the
kernel `ingest` capability (llmobs_plugin.KernelClient). The whole contract is
spoken over HTTP via the public protocol — no kernel internals.

Config (env):
  LLMOBS_KERNEL_URL      kernel base URL (e.g. http://kernel:8080)
  LLMOBS_SERVICE_TOKEN   this plugin's service token. NOTE (finding #4): the
                         kernel mints this at handshake but the delivery mechanism
                         to the plugin is not yet specified — for now the operator
                         provides it out of band; a kernel->plugin token push is
                         the proposed fix (see language-agnostic-findings.md).
"""

from __future__ import annotations

import os

from fastapi import FastAPI, Request, Response

from llmobs_plugin import KernelClient
from translate import batch_to_otlp

PLUGIN_ID = "llmobs/langfuse-compat"
app = FastAPI()

import time as _time

# Seed the watermark to startup so it is informative rather than a misleading 0.
# NOTE: langfuse-compat is idle-until-triggered (it makes no progress until a
# Langfuse client sends traffic), so its manifest declares NO watermarkBudget — an
# event-driven ingest plugin must not be degraded for being idle (the B3 lesson).
_last_progress = {"unix": int(_time.time())}
# The service token is DELIVERED by the kernel (H7c) — the plugin does not fetch
# it. Seed from env only as a fallback for local dev.
_token = {"value": os.environ.get("LLMOBS_SERVICE_TOKEN", "")}


@app.post("/plugin/v1/token")
async def receive_token(request: Request):
    """Kernel-initiated service-token delivery (H7c). The kernel PUSHES the token
    to this endpoint at handshake completion + on refresh; there is no pull path."""
    body = await request.json()
    _token["value"] = body.get("serviceToken", "")
    return Response(status_code=200)


def _kernel() -> KernelClient:
    return KernelClient(os.environ["LLMOBS_KERNEL_URL"], _token["value"])


@app.get("/plugin/v1/info")
def info():
    # Handshake self-report (api/plugin/v1alpha1/handshake.schema.json).
    return {
        "id": PLUGIN_ID,
        "version": "0.1.0",
        "pluginApiVersion": "v1alpha1",
        # MUST be a subset of the manifest's granted capabilities (the supervisor
        # rejects over-reach). langfuse-compat is backend-only: ingest only.
        "capabilities": ["ingest"],
        "displayName": "Langfuse compatibility",
    }


@app.get("/plugin/v1/health")
def health():
    import time

    # Two-signal health: live/ready + a functional watermark (last accepted event).
    return {
        "live": True,
        "ready": bool(os.environ.get("LLMOBS_KERNEL_URL")),
        "watermark": {"lastProgressUnix": _last_progress["unix"], "detail": "last langfuse batch"},
    }


@app.post("/api/public/ingestion")
async def ingestion(request: Request):
    """The Langfuse public ingestion endpoint an unmodified SDK posts to."""
    import time

    payload = await request.json()
    batch = payload.get("batch", [])
    otlp_json, skipped = batch_to_otlp(batch)
    _kernel().ingest_otlp(otlp_json)
    _last_progress["unix"] = int(time.time())
    # Langfuse SDK expects a 207-ish body listing per-event results; a 200 with
    # successes/errors is accepted by the client.
    return {"successes": [{"id": e.get("id"), "status": 201} for e in batch], "errors": [], "skipped": skipped}


@app.post("/plugin/v1/job")
def job(_: Request):
    # Placeholder job endpoint (the scheduler POSTs here); no-op for the demo.
    return Response(status_code=200)
