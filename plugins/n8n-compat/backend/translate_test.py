"""n8n translation tests — run: python3 translate_test.py (no external deps).

The fixture is the shape n8n's `GET /executions/{id}?includeData=true` returns for a
small workflow: a trigger node feeding an HTTP node feeding a (failed) code node.
"""

import json

from translate import execution_to_otlp, _hex_id, _root_span_id, _node_span_id

EXECUTION = {
    "id": "1042",
    "finished": True,
    "mode": "trigger",
    "status": "error",
    "startedAt": "2026-07-19T12:00:00.000Z",
    "stoppedAt": "2026-07-19T12:00:03.500Z",
    "workflowData": {"id": "17", "name": "Nightly sync"},
    "data": {
        "resultData": {
            "runData": {
                "Schedule Trigger": [
                    {"startTime": "2026-07-19T12:00:00.000Z", "executionTime": 5, "source": []}
                ],
                "HTTP Request": [
                    {
                        "startTime": "2026-07-19T12:00:00.010Z",
                        "executionTime": 1200,
                        "source": [{"previousNode": "Schedule Trigger", "previousNodeRun": 0}],
                    }
                ],
                "Code": [
                    {
                        "startTime": "2026-07-19T12:00:01.220Z",
                        "executionTime": 30,
                        "source": [{"previousNode": "HTTP Request", "previousNodeRun": 0}],
                        "error": {"message": "TypeError: undefined is not a function"},
                    }
                ],
            }
        }
    },
}


def _spans(execution):
    otlp_json, warnings = execution_to_otlp(execution)
    otlp = json.loads(otlp_json)
    spans = otlp["resourceSpans"][0]["scopeSpans"][0]["spans"]
    return {s["name"]: s for s in spans}, spans, warnings


def test_workflow_becomes_trace_with_node_spans():
    by_name, spans, warnings = _spans(EXECUTION)
    assert not warnings, warnings
    # 1 workflow root + 3 node spans.
    assert len(spans) == 4, [s["name"] for s in spans]

    trace_id = _hex_id("1042", 16)
    for s in spans:
        assert s["traceId"] == trace_id  # every span shares the one derived trace id

    root = by_name["Nightly sync"]
    assert root["spanId"] == _root_span_id("1042")
    rattrs = {a["key"]: a["value"] for a in root["attributes"]}
    assert rattrs["n8n.execution_id"]["stringValue"] == "1042"  # original id preserved
    assert rattrs["n8n.workflow_id"]["stringValue"] == "17"
    assert rattrs["n8n.raw.mode"]["stringValue"] == "trigger"  # n8n-specific -> raw
    # The execution failed, so the workflow root is error.
    assert root["status"]["code"] == "STATUS_CODE_ERROR"


def test_node_parentage_follows_the_graph():
    by_name, _, _ = _spans(EXECUTION)
    trig, http, code = by_name["Schedule Trigger"], by_name["HTTP Request"], by_name["Code"]

    # The trigger has no source -> parents to the workflow root.
    assert trig["parentSpanId"] == _root_span_id("1042")
    # HTTP parents to the trigger; Code parents to HTTP — the node graph is the span tree.
    assert http["parentSpanId"] == _node_span_id("1042", "Schedule Trigger", 0)
    assert code["parentSpanId"] == _node_span_id("1042", "HTTP Request", 0)


def test_node_timing_and_error_status():
    by_name, _, _ = _spans(EXECUTION)
    http = by_name["HTTP Request"]
    # end = start + executionTime(1200ms) -> a 1.2s span.
    assert int(http["endTimeUnixNano"]) - int(http["startTimeUnixNano"]) == 1_200_000_000

    code = by_name["Code"]
    assert code["status"]["code"] == "STATUS_CODE_ERROR"
    assert "TypeError" in code["status"]["message"]
    # A successful node is OK.
    assert by_name["HTTP Request"]["status"]["code"] == "STATUS_CODE_OK"


def test_missing_id_raises():
    try:
        execution_to_otlp({"workflowData": {"name": "x"}})
    except ValueError:
        return
    raise AssertionError("an execution with no id must raise")


def test_fan_out_is_bounded():
    """A pathological execution cannot fan out unboundedly (DoS): the span count is capped
    and the truncation is surfaced as a warning, never a silent drop or an OOM."""
    from translate import MAX_SPANS

    huge = {
        "id": "9",
        "status": "success",
        "finished": True,
        "startedAt": "2026-07-19T12:00:00.000Z",
        "workflowData": {"name": "huge"},
        "data": {"resultData": {"runData": {f"node{i}": [{"startTime": "2026-07-19T12:00:00.000Z", "executionTime": 0, "source": []}] for i in range(10000)}}},
    }
    otlp_json, warnings = execution_to_otlp(huge)
    spans = json.loads(otlp_json)["resourceSpans"][0]["scopeSpans"][0]["spans"]
    assert len(spans) <= MAX_SPANS + 1, len(spans)  # +1 for the workflow root
    assert any("cap" in w for w in warnings), warnings


def test_non_numeric_previous_run_does_not_crash():
    """previousNodeRun is attacker-influenceable; a non-int must not raise."""
    ex = {
        "id": "8",
        "status": "success",
        "finished": True,
        "startedAt": "2026-07-19T12:00:00.000Z",
        "workflowData": {"name": "w"},
        "data": {"resultData": {"runData": {"A": [{"startTime": "2026-07-19T12:00:00.000Z", "executionTime": 1, "source": [{"previousNode": "B", "previousNodeRun": "not-a-number"}]}]}}},
    }
    otlp_json, _ = execution_to_otlp(ex)  # must not raise
    assert json.loads(otlp_json)["resourceSpans"][0]["scopeSpans"][0]["spans"]


if __name__ == "__main__":
    test_workflow_becomes_trace_with_node_spans()
    test_node_parentage_follows_the_graph()
    test_node_timing_and_error_status()
    test_missing_id_raises()
    _, spans, _ = _spans(EXECUTION)
    print(f"translate OK: workflow -> {len(spans)} spans (1 workflow + 3 nodes), graph preserved")
