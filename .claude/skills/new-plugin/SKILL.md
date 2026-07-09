---
name: new-plugin
description: Scaffold a new plugin from templates/, wire its manifest, register it in the compose overlay, and add a conformance entry. Use when adding a first-party plugin or demonstrating the third-party authoring flow.
---

# New plugin

First-party plugins are structured exactly like third-party ones. Scaffold from
`templates/`, never by hand-copying kernel code.

## Steps

1. **Choose a template** in `templates/`:
   - `plugin-python/` — FastAPI backend + MF frontend + manifest + CI workflow.
   - `plugin-typescript/` — TS backend + MF frontend.
   - `plugin-frontend-only/` — Tier-2, no backend.
2. **Scaffold** (the CLI drives this once implemented):
   ```sh
   llmobs plugin create <owner>/<plugin-name> --template <template>
   ```
   For a first-party plugin the target is `plugins/<name>/`.
3. **Fill the manifest** (`llmobs-plugin.yaml`): namespaced id `owner/plugin-
   name`, requested capabilities (only the data primitives it needs), surfaces,
   and settings schema. It must validate against
   `api/schemas/manifest/v1alpha1`.
4. **Respect the boundary.** Backend imports only `pkg/pluginproto` + `pkg/
   model`; frontend imports only `@llmobs/plugin-sdk` (+ `@llmobs/ui`,
   `@llmobs/tokens`). No infrastructure access.
5. **Register in the lite compose overlay** under `deploy/compose/` so the
   plugin runs in the shared runtime for local dev.
6. **Add a conformance entry** in `tools/conformance/` so CI verifies the plugin
   against the "verified" bar.
7. **Add the Go module** to `go.work` (`go work use ./plugins/<name>/backend`)
   and the frontend is already covered by `pnpm-workspace.yaml`.
8. **Docs.** Add/update `docs/plugin-authors/` if this exercises new API.

## Done when
- Manifest validates, boundary lint passes, plugin appears in `make dev`,
  conformance covers it, and (if plugin-facing API changed) the DoD for a
  plugin-facing change is met.
