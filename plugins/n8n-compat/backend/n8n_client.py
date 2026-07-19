"""Minimal n8n public REST client — the source side of the compat plugin.

n8n exposes an execution API (https://docs.n8n.io/api/): list executions and fetch one
with its full run data. We only need two calls: list finished executions newer than our
cursor, and (implicitly, via includeData) their per-node run data. Auth is the n8n API
key in the `X-N8N-API-KEY` header. This client is pure transport — the mapping lives in
translate.py — so it is trivially swappable/mocked in tests.
"""

from __future__ import annotations

from typing import Any
from urllib.parse import quote


class N8nClient:
    def __init__(self, base_url: str, api_key: str, client: Any = None, timeout: float = 15.0):
        import httpx  # lazy: only the live path needs the HTTP stack

        self.base_url = base_url.rstrip("/")
        self._headers = {"X-N8N-API-KEY": api_key, "Accept": "application/json"}
        self._c = client or httpx.Client(timeout=timeout)

    def list_since(self, after_id: str | None, limit: int = 50) -> list[dict]:
        """Return executions of ANY status (success/error/running/…) newer than after_id,
        ASCENDING by id (oldest-first — the order the caller processes and gates on
        completion). No status filter: failed workflow runs are exactly what an
        observability user wants, and the caller stops at the first still-running one so
        the id cursor never leapfrogs an unfinished older execution. Bounded to `limit`.
        """
        collected: list[dict] = []
        cursor: str | None = None
        while len(collected) < limit:
            params: dict[str, Any] = {"includeData": "false", "limit": min(100, limit)}
            if cursor:
                params["cursor"] = cursor
            r = self._c.get(f"{self.base_url}/api/v1/executions", params=params, headers=self._headers)
            r.raise_for_status()
            body = r.json()
            page = body.get("data") or []
            reached = False
            for ex in page:
                if after_id is not None and str(ex.get("id")) == str(after_id):
                    reached = True
                    break
                collected.append(ex)
                if len(collected) >= limit:
                    break
            cursor = body.get("nextCursor")
            if reached or not cursor or not page:
                break
        collected.reverse()  # n8n returns newest-first; hand back oldest-first
        return collected

    def get_execution(self, execution_id: str) -> dict:
        """Fetch one execution WITH its run data (the node graph we translate)."""
        r = self._c.get(
            f"{self.base_url}/api/v1/executions/{quote(str(execution_id), safe='')}",
            params={"includeData": "true"},
            headers=self._headers,
        )
        r.raise_for_status()
        return r.json()
