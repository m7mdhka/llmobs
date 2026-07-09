# templates/

Scaffolds used by `llmobs plugin create`. First-party plugins in `plugins/` are
structured exactly like what these templates produce — so a third party and a
maintainer author plugins the same way.

| Template | What it scaffolds |
|---|---|
| `plugin-python/` | FastAPI backend + Module Federation frontend + manifest + CI workflow. |
| `plugin-typescript/` | TypeScript backend + MF frontend + manifest + CI workflow. |
| `plugin-frontend-only/` | Tier-2: no backend — a frontend-only surface plus manifest. |

Everything a template emits is within the plugin boundary (public plugin API
only). See the `new-plugin` skill and
[`docs/plugin-authors/`](../docs/plugin-authors/).
