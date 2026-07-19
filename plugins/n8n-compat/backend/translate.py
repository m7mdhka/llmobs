"""n8n execution -> OTLP translation (the n8n tracing core).

The #1 community request in the incumbent's tracker is n8n tracing, and the
incumbent structurally cannot ship it: n8n emits no OpenTelemetry, so there is no
wire dialect to normalize on the hot path. In a kernel-plus-plugins model it is a
COLD-PATH COMPAT PLUGIN: this plugin polls n8n's execution REST API
(`GET /executions/{id}?includeData=true`) and translates a finished workflow run
into OTLP spans server-side — a container + a manifest, no upstream instrumentation
required. This module is the pure, fixture-tested translation core (no I/O); the
poller that fetches executions and pushes the OTLP through the `ingest` capability
lives in app.py.

Mapping (workflow run -> trace):
  - the execution is a trace; its ROOT span is the workflow itself
    (name = workflowData.name), spanning startedAt -> stoppedAt, error status if the
    execution failed.
  - each node run in `data.resultData.runData[nodeName][i]` is a CHILD span: name =
    the node name, start = the run's startTime, end = startTime + executionTime(ms),
    parent = the node that fed it (run.source[].previousNode) or the workflow root,
    error status if the run carries an `error`.
  - n8n ids are arbitrary strings; OTLP ids are 16/8 bytes, so we DERIVE stable hex
    ids by hashing and preserve the originals under `n8n.execution_id` /
    `n8n.node.name` so nothing is lost — the id itself does not round-trip byte-for-
    byte (the same structural finding the Langfuse compat plugin documents).
"""

from __future__ import annotations

import hashlib
import json
from datetime import datetime
from typing import Any

# DoS bounds: a compromised or buggy n8n could return an execution with an enormous
# runData map. Cap the fan-out and the attribute-value size so one execution can never
# exhaust the poller's memory or build a multi-GB ingest body. Anything truncated is
# surfaced as a warning rather than silently dropped.
MAX_NODES = 2000
MAX_RUNS_PER_NODE = 200
MAX_SPANS = 5000
MAX_ATTR_STR = 8192


def _hex_id(s: str, nbytes: int) -> str:
    return hashlib.sha256(s.encode()).hexdigest()[: nbytes * 2]


def _iso_to_nano(ts: Any) -> str:
    """n8n timestamps arrive as ISO-8601 strings or epoch-millis numbers."""
    if ts is None:
        return "0"
    if isinstance(ts, (int, float)):
        return str(int(ts * 1e6))  # epoch millis -> nanos
    s = str(ts).replace("Z", "+00:00")
    try:
        return str(int(datetime.fromisoformat(s).timestamp() * 1e9))
    except ValueError:
        return "0"


def _add_nanos(start_nano: str, millis: Any) -> str:
    try:
        return str(int(start_nano) + int(float(millis) * 1e6))
    except (ValueError, TypeError):
        return start_nano


def _clip(s: str) -> str:
    """Bound an attribute string so a pathological node value can't build a huge body."""
    return s if len(s) <= MAX_ATTR_STR else s[:MAX_ATTR_STR] + "…[truncated]"


def _kv(key: str, value: Any) -> dict:
    if isinstance(value, bool):
        return {"key": key, "value": {"boolValue": value}}
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        return {"key": key, "value": {"doubleValue": float(value)}}
    if isinstance(value, str):
        return {"key": key, "value": {"stringValue": _clip(value)}}
    return {"key": key, "value": {"stringValue": _clip(json.dumps(value, default=str))}}


def _root_span_id(exec_id: str) -> str:
    return _hex_id(exec_id + ":root", 8)


def _node_span_id(exec_id: str, node: str, run_index: int) -> str:
    return _hex_id(f"{exec_id}:{node}:{run_index}", 8)


def _workflow_root(execution: dict, trace_id: str) -> dict:
    exec_id = str(execution["id"])
    wf = execution.get("workflowData") or {}
    attrs = [
        _kv("n8n.execution_id", exec_id),
        _kv("llmobs.span.kind", "workflow"),
    ]
    if wf.get("id") is not None:
        attrs.append(_kv("n8n.workflow_id", str(wf["id"])))
    if execution.get("mode"):  # n8n-specific (manual/trigger/webhook): raw, no canonical field
        attrs.append(_kv("n8n.raw.mode", execution["mode"]))
    start = _iso_to_nano(execution.get("startedAt"))
    span = {
        "traceId": trace_id,
        "spanId": _root_span_id(exec_id),
        "name": wf.get("name") or "workflow",
        "startTimeUnixNano": start,
        "endTimeUnixNano": _iso_to_nano(execution.get("stoppedAt")) or start,
        "attributes": attrs,
    }
    if execution.get("status") == "error" or execution.get("finished") is False:
        span["status"] = {"code": "STATUS_CODE_ERROR"}
    else:
        span["status"] = {"code": "STATUS_CODE_OK"}
    return span


def _node_span(execution: dict, trace_id: str, node: str, run_index: int, run: dict) -> dict:
    exec_id = str(execution["id"])
    start = _iso_to_nano(run.get("startTime"))
    attrs = [
        _kv("n8n.node.name", node),
        _kv("n8n.node.run_index", run_index),
        _kv("llmobs.span.kind", "node"),
    ]
    # Parent: the node that fed this run, else the workflow root.
    parent = _root_span_id(exec_id)
    src = run.get("source") or []
    if src and isinstance(src, list) and src[0] and src[0].get("previousNode"):
        prev = src[0]["previousNode"]
        try:  # previousNodeRun is attacker-influenceable; a non-int must not crash the run
            prev_run = int(src[0].get("previousNodeRun") or 0)
        except (ValueError, TypeError):
            prev_run = 0
        parent = _node_span_id(exec_id, prev, prev_run)
        attrs.append(_kv("n8n.node.previous", prev))

    span = {
        "traceId": trace_id,
        "spanId": _node_span_id(exec_id, node, run_index),
        "parentSpanId": parent,
        "name": node,
        "startTimeUnixNano": start,
        "endTimeUnixNano": _add_nanos(start, run.get("executionTime", 0)),
        "attributes": attrs,
    }
    if run.get("error"):
        span["status"] = {"code": "STATUS_CODE_ERROR"}
        err = run["error"]
        msg = err.get("message") if isinstance(err, dict) else str(err)
        if msg:
            span["status"]["message"] = str(msg)
    else:
        span["status"] = {"code": "STATUS_CODE_OK"}
    return span


def execution_to_otlp(execution: dict) -> tuple[str, list[str]]:
    """Translate ONE finished n8n execution into an OTLP/JSON trace export.

    Returns (otlp_json, warnings) where warnings names structural gaps that were
    handled but are worth surfacing (e.g. a node run with no startTime).
    """
    if not execution.get("id"):
        raise ValueError("execution has no id")
    exec_id = str(execution["id"])
    trace_id = _hex_id(exec_id, 16)
    warnings: list[str] = []

    spans: list[dict] = [_workflow_root(execution, trace_id)]

    run_data = (((execution.get("data") or {}).get("resultData") or {}).get("runData")) or {}
    # Deterministic node order so the emitted export is stable for fixtures. Bounded so a
    # pathological execution cannot fan out unboundedly (DoS): cap nodes, runs-per-node,
    # and total spans, surfacing any truncation as a warning rather than a silent drop.
    nodes = sorted(run_data.keys())
    if len(nodes) > MAX_NODES:
        warnings.append(f"node count {len(nodes)} exceeds cap {MAX_NODES}; truncated")
        nodes = nodes[:MAX_NODES]
    for node in nodes:
        if len(spans) >= MAX_SPANS:
            warnings.append(f"span count hit cap {MAX_SPANS}; remaining nodes dropped")
            break
        runs = run_data[node] or []
        if not isinstance(runs, list):
            warnings.append(f"{node}: runData not a list")
            continue
        if len(runs) > MAX_RUNS_PER_NODE:
            warnings.append(f"{node}: run count {len(runs)} exceeds cap {MAX_RUNS_PER_NODE}; truncated")
            runs = runs[:MAX_RUNS_PER_NODE]
        for i, run in enumerate(runs):
            if len(spans) >= MAX_SPANS:
                break
            if not isinstance(run, dict):
                warnings.append(f"{node}[{i}]: run not an object")
                continue
            if run.get("startTime") is None:
                warnings.append(f"{node}[{i}]: no startTime")
            spans.append(_node_span(execution, trace_id, node, i, run))

    export = {
        "resourceSpans": [
            {
                "resource": {"attributes": [_kv("service.name", "n8n")]},
                "scopeSpans": [{"scope": {"name": "llmobs/n8n-compat"}, "spans": spans}],
            }
        ]
    }
    return json.dumps(export), warnings
