# LLMObs MCP server

A first-party [Model Context Protocol](https://modelcontextprotocol.io) server
that exposes **read-only, metadata-safe** tools over the LLMObs Query API, so an
AI assistant can reason about your LLM system directly — *"why did checkout-agent
get slow yesterday?"*

It speaks JSON-RPC 2.0 over stdio (the standard MCP transport) and is a single Go
binary with no dependencies.

## Safety by construction

The server **requires a metadata-scoped API key**. Payload redaction is enforced
by the kernel (PR-F2), so nothing these tools return contains prompt/response
text. On startup the server checks its key via `/v1alpha1/whoami`; if the key can
read payloads it **refuses to start** unless you pass `--allow-payloads` to
explicitly accept exposing prompts/PII to the assistant.

Issue a metadata key (admin session):

```bash
curl -X POST -b cookies -H "X-CSRF-Token: $CSRF" -H "Content-Type: application/json" \
  -d '{"scopes":["query"]}' http://localhost:8080/v1alpha1/api-keys
# -> {"secret":"sk-...", "public_key":"pk-...", "scopes":["query"]}
```

## Tools

| Tool | Returns (metadata only) |
|---|---|
| `query_traces` | Recent traces: id, name, start, duration, span_count, environment, status, is_open, incomplete. Named filters + cursor paging. |
| `get_trace_tree` | One trace's spans in tree order: id, parent, name, kind, duration, status. |
| `list_recent_errors` | Recent error spans: trace_id, span_id, name, kind, start, model. |
| `top_costs` | `sum(total_cost)` grouped by model/provider/environment/session/user, top N. |
| `get_span` | One span's metadata: times, duration, status, model, usage, cost. |

Tool inputs are **named common filters**, not raw query language, and results are
**capped and summarized** (default 20 rows, max 100) — an assistant gets what it
needs to reason, then drills in with `get_trace_tree`/`get_span`.

## Configure an MCP client (e.g. Claude Desktop)

```json
{
  "mcpServers": {
    "llmobs": {
      "command": "/path/to/llmobs-mcp",
      "env": {
        "LLMOBS_URL": "http://localhost:8080",
        "LLMOBS_API_KEY": "sk-your-metadata-scoped-key"
      }
    }
  }
}
```

Build: `go build -o llmobs-mcp ./tools/mcp-server`.
