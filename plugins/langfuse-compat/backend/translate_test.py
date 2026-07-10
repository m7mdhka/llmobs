"""Translation tests — run: python3 translate_test.py (no external deps)."""

import json

from translate import batch_to_otlp, _hex_id

# A batch an unmodified Langfuse SDK would send.
BATCH = [
    {"type": "trace-create", "body": {"id": "trace-abc", "name": "chat", "userId": "u-42", "timestamp": "2026-07-10T14:30:00Z", "public": True, "metadata": {"env": "prod"}}},
    {"type": "generation-create", "body": {"id": "gen-1", "traceId": "trace-abc", "name": "llm", "model": "gpt-4o", "usage": {"input": 812, "output": 40, "unit": "TOKENS"}, "startTime": "2026-07-10T14:30:00Z", "endTime": "2026-07-10T14:30:01Z", "level": "DEFAULT"}},
    {"type": "score-create", "body": {"id": "s1", "traceId": "trace-abc", "name": "quality", "value": 0.9}},
]


def test_batch_translation():
    otlp_json, skipped = batch_to_otlp(BATCH)
    otlp = json.loads(otlp_json)
    spans = otlp["resourceSpans"][0]["scopeSpans"][0]["spans"]
    assert len(spans) == 2, "trace + generation map to spans; score does not"
    # score-create is honestly reported as not-mapped-through-ingest.
    assert "score-create" in skipped, skipped

    root = spans[0]
    gen = spans[1]
    # id derivation: trace id is a 32-hex hash of the Langfuse id; original preserved.
    assert root["traceId"] == _hex_id("trace-abc", 16)
    attrs = {a["key"]: a["value"] for a in root["attributes"]}
    assert attrs["langfuse.trace_id"]["stringValue"] == "trace-abc"
    assert attrs["user.id"]["stringValue"] == "u-42"
    assert attrs["langfuse.raw.public"]["boolValue"] is True  # Langfuse-specific -> raw

    # generation shares the trace, parents to the root, carries gen_ai semconv.
    assert gen["traceId"] == root["traceId"]
    assert gen["parentSpanId"] == root["spanId"]
    gattrs = {a["key"]: a["value"] for a in gen["attributes"]}
    assert gattrs["gen_ai.request.model"]["stringValue"] == "gpt-4o"
    assert gattrs["gen_ai.usage.input_tokens"]["doubleValue"] == 812.0


if __name__ == "__main__":
    test_batch_translation()
    otlp_json, skipped = batch_to_otlp(BATCH)
    print("translate OK: 2 spans emitted, skipped =", skipped)
