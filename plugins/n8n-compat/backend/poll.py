"""The poll cycle — framework-free so it is unit-testable without the web/crypto stack.

app.py wires this into an HTTP handler; poll_test.py drives it with fake clients. The
`kernel` argument is duck-typed (anything with secret_get / kv_get / kv_set / ingest_otlp)
so the test needs no real KernelClient.

Failure taxonomy (the invariant this plugin must honor): a PERMANENT failure on one
execution — a malformed execution that can never translate, or an ingest the kernel 4xx-
rejects as malformed OTLP — must be dead-lettered and the cursor advanced PAST it, so it
cannot head-of-line-block every newer execution forever. A TRANSIENT failure (n8n or the
kernel momentarily unreachable, a 5xx) aborts the cycle WITHOUT advancing, so it is
retried on the next tick and never dropped.
"""

from __future__ import annotations

from n8n_client import N8nClient
from translate import execution_to_otlp

CURSOR_KEY = "n8n_poll_cursor"  # kv key holding the last-processed execution id
API_KEY_SECRET = "n8n_api_key"  # secrets name the operator stores the n8n key under
MAX_PER_CYCLE = 50


def default_n8n_factory(api_key: str) -> N8nClient:
    import os

    return N8nClient(os.environ["N8N_API_URL"], api_key)


def _is_finished(summary: dict) -> bool:
    """A run is ready to trace once n8n reports it finished (success or error)."""
    if summary.get("finished") is False:
        return False
    return summary.get("status") not in ("running", "waiting", "new", None) or summary.get("finished") is True


def _is_permanent(e: Exception) -> bool:
    """A permanent (dead-letter) failure: a malformed execution (translate ValueError) or
    an ingest the kernel rejected 4xx (malformed OTLP it will always reject). Everything
    else — network errors, 5xx — is transient."""
    if isinstance(e, ValueError):
        return True
    code = getattr(e, "status_code", None)
    if code is None:
        code = getattr(getattr(e, "response", None), "status_code", None)
    return code is not None and 400 <= code < 500


def run_poll_cycle(kernel, assertion: str, n8n_factory=default_n8n_factory) -> dict:
    """One poll cycle:
      1. read the n8n API key (secrets) and the last-seen execution id (kv cursor);
      2. list executions newer than the cursor, oldest-first;
      3. STOP at the first still-running execution (the id cursor must not leapfrog an
         unfinished older run — that would lose it);
      4. for each finished execution: translate + ingest; on a PERMANENT failure skip it
         and advance the cursor (dead-letter); on a TRANSIENT failure abort the cycle so
         the next tick retries without advancing.
    Returns a summary used for the response + the health watermark.
    """
    api_key = kernel.secret_get(assertion, API_KEY_SECRET)
    cursor = kernel.kv_get(assertion, CURSOR_KEY)
    if not api_key:
        return {"processed": 0, "skipped": 0, "cursor": cursor, "detail": f"no {API_KEY_SECRET} secret set", "warnings": []}

    n8n = n8n_factory(api_key)
    unseen = n8n.list_since(after_id=cursor, limit=MAX_PER_CYCLE)  # oldest-first

    processed = skipped = 0
    newest_id = cursor
    warnings: list[str] = []
    for summary in unseen:
        exec_id = str(summary.get("id"))
        if not _is_finished(summary):
            # Wait for it (and everything after it) to finish; do not advance past.
            break
        try:
            execution = n8n.get_execution(exec_id)
            otlp_json, _warn = execution_to_otlp(execution)
            kernel.ingest_otlp(otlp_json)
        except Exception as e:  # noqa: BLE001 — classify, don't swallow blindly
            if _is_permanent(e):
                # Dead-letter: this execution can never succeed; skip and advance so it
                # cannot block every newer execution forever.
                warnings.append(f"{exec_id}: dead-lettered (permanent): {type(e).__name__}")
                kernel.kv_set(assertion, CURSOR_KEY, exec_id)
                newest_id = exec_id
                skipped += 1
                continue
            # Transient: stop here WITHOUT advancing; the next tick retries this exec_id.
            warnings.append(f"{exec_id}: transient failure, will retry: {type(e).__name__}")
            break
        # Advance after each success so a crash resumes from the last ingested id.
        kernel.kv_set(assertion, CURSOR_KEY, exec_id)
        newest_id = exec_id
        processed += 1

    detail = f"ingested {processed}, dead-lettered {skipped}"
    return {"processed": processed, "skipped": skipped, "cursor": newest_id, "detail": detail, "warnings": warnings}
