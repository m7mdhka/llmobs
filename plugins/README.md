# plugins/

First-party plugins. **The dogfood rule (D2) applies:** each is structured
*exactly* like a third-party plugin would be — `backend/` (Go), `frontend/`
(TS), a manifest (`llmobs-plugin.yaml`), `migrations/`, and tests. They use ONLY
the public plugin API (`pkg/pluginproto`, `pkg/model`, `@llmobs/plugin-sdk`) and
get no private kernel access, ever.

This is also the escape hatch: any first-party plugin can be extracted to its own
repo later with zero restructuring.

| Plugin | Purpose |
|---|---|
| `tracing/` | Trace/span exploration UI + backend. |
| `evals/` | Evaluation runs and scores. |
| `prompt-management/` | Prompt versioning and management. |
| `langfuse-compat/` | Compat endpoint plugin (D3) — holds `ingest:write` capability; accepts Langfuse wire format on its own endpoint (cold path). |

When copying one of these as a reference for your own plugin, everything you see
is legal for you to do. See
[`.claude/rules/plugin-boundary.md`](../.claude/rules/plugin-boundary.md) and the
`new-plugin` skill.
