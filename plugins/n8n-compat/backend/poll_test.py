"""Poll-cycle orchestration tests — run: python3 poll_test.py (no external deps).

Uses fake kernel + n8n clients so the cursor / ordering / failure-taxonomy logic is
tested without a live n8n or kernel.
"""

import json

from poll import run_poll_cycle, CURSOR_KEY, API_KEY_SECRET
from llmobs_plugin import IngestError


def _exec(exec_id, status="success"):
    return {
        "id": exec_id,
        "status": status,
        "finished": True,
        "startedAt": "2026-07-19T12:00:00.000Z",
        "stoppedAt": "2026-07-19T12:00:01.000Z",
        "workflowData": {"id": "1", "name": f"wf-{exec_id}"},
        "data": {"resultData": {"runData": {"Trigger": [{"startTime": "2026-07-19T12:00:00.000Z", "executionTime": 1, "source": []}]}}},
    }


class FakeKernel:
    def __init__(self, secret="n8n-key", cursor=None, ingest=None):
        self._secrets = {API_KEY_SECRET: secret}
        self._kv = {CURSOR_KEY: cursor} if cursor is not None else {}
        self.ingested = []
        self._ingest = ingest  # optional: callable(otlp)->raise to simulate failures

    def secret_get(self, assertion, name):
        return self._secrets.get(name)

    def kv_get(self, assertion, key):
        return self._kv.get(key)

    def kv_set(self, assertion, key, value):
        self._kv[key] = value

    def ingest_otlp(self, otlp_json):
        if self._ingest:
            self._ingest(otlp_json)
        self.ingested.append(json.loads(otlp_json))


class FakeN8n:
    """`listing` is what list_since returns (already oldest-first). `execs` maps id->full."""

    def __init__(self, listing, execs=None):
        self._listing = listing
        self._execs = execs or {}

    def list_since(self, after_id, limit=50):
        return list(self._listing)

    def get_execution(self, execution_id):
        return self._execs.get(str(execution_id), _exec(execution_id))


def _names(kernel):
    return [e["resourceSpans"][0]["scopeSpans"][0]["spans"][0]["name"] for e in kernel.ingested]


def test_processes_oldest_first_and_advances_cursor():
    k = FakeKernel(cursor=None)
    listing = [{"id": "10", "status": "success", "finished": True}, {"id": "11", "status": "success", "finished": True}]
    summary = run_poll_cycle(k, "a", n8n_factory=lambda key: FakeN8n(listing))
    assert summary["processed"] == 2, summary
    assert summary["cursor"] == "11"
    assert k._kv[CURSOR_KEY] == "11"
    assert _names(k) == ["wf-10", "wf-11"]


def test_error_executions_are_traced():
    k = FakeKernel(cursor=None)
    listing = [{"id": "10", "status": "error", "finished": True}]
    summary = run_poll_cycle(k, "a", n8n_factory=lambda key: FakeN8n(listing, {"10": _exec("10", status="error")}))
    assert summary["processed"] == 1, summary
    # the failed workflow's root span is ERROR — the run a user most wants to see.
    root = k.ingested[0]["resourceSpans"][0]["scopeSpans"][0]["spans"][0]
    assert root["status"]["code"] == "STATUS_CODE_ERROR"


def test_stops_at_first_unfinished():
    # 10 finished, 11 still running -> process 10, STOP before 11 (don't leapfrog it).
    k = FakeKernel(cursor=None)
    listing = [{"id": "10", "status": "success", "finished": True}, {"id": "11", "status": "running", "finished": False}]
    summary = run_poll_cycle(k, "a", n8n_factory=lambda key: FakeN8n(listing))
    assert summary["processed"] == 1
    assert summary["cursor"] == "10"  # cursor did NOT advance past the running execution


def test_permanent_failure_is_dead_lettered_and_unblocks():
    # 10 is a permanent poison (ingest 400); 11 is good. The poison must be skipped +
    # cursor advanced so 11 still gets ingested — no head-of-line block.
    def ingest(otlp):
        name = json.loads(otlp)["resourceSpans"][0]["scopeSpans"][0]["spans"][0]["name"]
        if name == "wf-10":
            raise IngestError(400)  # permanent: malformed OTLP the kernel rejects

    k = FakeKernel(cursor=None, ingest=ingest)
    listing = [{"id": "10", "status": "success", "finished": True}, {"id": "11", "status": "success", "finished": True}]
    summary = run_poll_cycle(k, "a", n8n_factory=lambda key: FakeN8n(listing))
    assert summary["skipped"] == 1 and summary["processed"] == 1, summary
    assert summary["cursor"] == "11"  # advanced past the poison AND the good one
    assert _names(k) == ["wf-11"]  # only 11 ingested; 10 dead-lettered


def test_transient_failure_aborts_without_advancing():
    # 10 fails transiently (ingest 503) -> cycle aborts, cursor stays before 10, retried.
    def ingest(otlp):
        raise IngestError(503)

    k = FakeKernel(cursor="9", ingest=ingest)
    listing = [{"id": "10", "status": "success", "finished": True}, {"id": "11", "status": "success", "finished": True}]
    summary = run_poll_cycle(k, "a", n8n_factory=lambda key: FakeN8n(listing))
    assert summary["processed"] == 0 and summary["skipped"] == 0
    assert k._kv[CURSOR_KEY] == "9"  # cursor unchanged -> 10 retried next tick, not dropped


def test_no_secret_is_a_noop():
    k = FakeKernel(secret=None)
    listing = [{"id": "10", "status": "success", "finished": True}]
    summary = run_poll_cycle(k, "a", n8n_factory=lambda key: FakeN8n(listing))
    assert summary["processed"] == 0 and k.ingested == []


if __name__ == "__main__":
    test_processes_oldest_first_and_advances_cursor()
    test_error_executions_are_traced()
    test_stops_at_first_unfinished()
    test_permanent_failure_is_dead_lettered_and_unblocks()
    test_transient_failure_aborts_without_advancing()
    test_no_secret_is_a_noop()
    print("poll OK: oldest-first, error runs traced, unfinished-gate, permanent dead-letter, transient retry, no-secret no-op")
