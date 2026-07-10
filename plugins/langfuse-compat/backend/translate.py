"""Langfuse wire-format -> OTLP translation (the migration core).

An UNMODIFIED Langfuse SDK client posts a batch of events to
`POST /api/public/ingestion`; we translate each into an OTLP span (GenAI semconv)
and push it through the kernel `ingest` capability. The exact field-by-field
fidelity (clean / llmobs.raw.* / doesn't-map / partial) is documented in
docs/plugin-authors/language-agnostic-findings.md — this module is its executable
form. "Migration is a config change" is only honest given that table.

Key structural finding: Langfuse ids are arbitrary strings; OTLP trace/span ids
are 16/8 bytes. We DERIVE a stable hex id by hashing and preserve the ORIGINAL in
`langfuse.trace_id` / `langfuse.observation_id` so nothing is lost — but the id
itself does not round-trip byte-for-byte.
"""

from __future__ import annotations

import hashlib
import json
from datetime import datetime
from typing import Any


def _hex_id(s: str, nbytes: int) -> str:
    return hashlib.sha256(s.encode()).hexdigest()[: nbytes * 2]


def _unix_nano(ts: str | None) -> str:
    if not ts:
        return "0"
    ts = ts.replace("Z", "+00:00")
    try:
        return str(int(datetime.fromisoformat(ts).timestamp() * 1e9))
    except ValueError:
        return "0"


def _kv(key: str, value: Any) -> dict:
    if isinstance(value, bool):
        return {"key": key, "value": {"boolValue": value}}
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        return {"key": key, "value": {"doubleValue": float(value)}}
    if isinstance(value, str):
        return {"key": key, "value": {"stringValue": value}}
    return {"key": key, "value": {"stringValue": json.dumps(value)}}


# Langfuse observation level -> our span status code.
_LEVEL_TO_STATUS = {"ERROR": "STATUS_CODE_ERROR", "WARNING": "STATUS_CODE_UNSET", "DEFAULT": "STATUS_CODE_UNSET", "DEBUG": "STATUS_CODE_UNSET"}


def _span_from_trace(body: dict) -> dict:
    """A Langfuse trace becomes the root span of its trace."""
    tid = _hex_id(body["id"], 16)
    attrs = [_kv("langfuse.trace_id", body["id"])]  # original id preserved (raw)
    for k in ("userId", "sessionId", "release", "version"):
        if body.get(k) is not None:
            attrs.append(_kv({"userId": "user.id", "sessionId": "session.id", "release": "service.version", "version": "llmobs.version"}[k], body[k]))
    if body.get("input") is not None:
        attrs.append(_kv("llmobs.input", body["input"]))
    if body.get("output") is not None:
        attrs.append(_kv("llmobs.output", body["output"]))
    for k, v in (body.get("metadata") or {}).items():
        attrs.append(_kv(f"langfuse.raw.metadata.{k}", v))
    if body.get("public") is not None:  # Langfuse-specific: no canonical mapping
        attrs.append(_kv("langfuse.raw.public", body["public"]))
    return {
        "traceId": tid,
        "spanId": _hex_id(body["id"] + ":root", 8),
        "name": body.get("name") or "trace",
        "startTimeUnixNano": _unix_nano(body.get("timestamp")),
        "endTimeUnixNano": _unix_nano(body.get("timestamp")),
        "attributes": attrs,
    }


def _span_from_observation(body: dict, kind: str) -> dict:
    tid = _hex_id(body["traceId"], 16)
    attrs = [_kv("langfuse.observation_id", body["id"]), _kv("gen_ai.operation.name", kind)]
    if kind == "generation":
        if body.get("model"):
            attrs.append(_kv("gen_ai.request.model", body["model"]))
        if body.get("modelParameters"):
            attrs.append(_kv("llmobs.model_parameters", body["modelParameters"]))
        usage = body.get("usage") or {}
        if usage.get("input") is not None:
            attrs.append(_kv("gen_ai.usage.input_tokens", usage["input"]))
        if usage.get("output") is not None:
            attrs.append(_kv("gen_ai.usage.output_tokens", usage["output"]))
        if usage.get("unit") and usage["unit"] != "TOKENS":  # CHARACTERS/etc: partial
            attrs.append(_kv("langfuse.raw.usage_unit", usage["unit"]))
        if body.get("completionStartTime"):
            attrs.append(_kv("llmobs.completion_start_time", body["completionStartTime"]))
        if body.get("promptName"):
            attrs.append(_kv("langfuse.raw.prompt_name", body["promptName"]))
    if body.get("input") is not None:
        attrs.append(_kv("llmobs.input", body["input"]))
    if body.get("output") is not None:
        attrs.append(_kv("llmobs.output", body["output"]))
    if body.get("statusMessage"):
        attrs.append(_kv("langfuse.raw.status_message", body["statusMessage"]))
    span = {
        "traceId": tid,
        "spanId": _hex_id(body["id"], 8),
        "name": body.get("name") or kind,
        "startTimeUnixNano": _unix_nano(body.get("startTime") or body.get("timestamp")),
        "endTimeUnixNano": _unix_nano(body.get("endTime") or body.get("startTime") or body.get("timestamp")),
        "attributes": attrs,
    }
    if body.get("parentObservationId"):
        span["parentSpanId"] = _hex_id(body["parentObservationId"], 8)
    else:
        span["parentSpanId"] = _hex_id(body["traceId"] + ":root", 8)
    level = (body.get("level") or "DEFAULT").upper()
    if _LEVEL_TO_STATUS.get(level) == "STATUS_CODE_ERROR":
        span["status"] = {"code": "STATUS_CODE_ERROR"}
    return span


# Langfuse ingestion event type -> handler.
_KIND = {"generation-create": "generation", "generation-update": "generation", "span-create": "span", "span-update": "span", "observation-create": "span", "event-create": "span"}

# Types that do NOT map through the OTLP/ingest path (documented caveat).
UNMAPPED_TYPES = {"score-create", "sdk-log"}


def batch_to_otlp(batch: list[dict]) -> tuple[str, list[str]]:
    """Translate a Langfuse ingestion batch into an OTLP/JSON trace export.

    Returns (otlp_json, skipped) where skipped names event types that do not map
    through ingest (e.g. score-create -> use the score-write primitive).
    """
    spans: list[dict] = []
    skipped: list[str] = []
    for ev in batch:
        etype = ev.get("type", "")
        body = ev.get("body") or {}
        if etype == "trace-create":
            spans.append(_span_from_trace(body))
        elif etype in _KIND:
            if not body.get("traceId"):
                skipped.append(etype + " (no traceId)")
                continue
            spans.append(_span_from_observation(body, _KIND[etype]))
        else:
            skipped.append(etype)
    export = {"resourceSpans": [{"scopeSpans": [{"spans": spans}]}]}
    return json.dumps(export), skipped
