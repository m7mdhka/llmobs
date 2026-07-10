# @llmobs/plugin-sdk changelog

Semver-sacred. The plugin SDK is the Tier-2 public surface; breaking changes
require a major bump and a deprecation note.

## 0.1.0 — initial (PR-D4)

The v0 API, empirically derived from what the first-party tracing plugin needed:

- `LLMObsPluginProvider` / `useLLMObs` — the runtime context (project, base URL,
  user, data client) the shell supplies to every plugin surface.
- `useQuery(doc)`, `useTraces(params)`, `useTrace(id)` — the `query` primitive as
  React hooks, each returning `{ data, loading, error, refetch }` so surfaces
  render the standard states uniformly.
- `DataClient`, `SdkError`, `QueryInput`, `QueryResponse`, `TraceTree` — the
  same-origin, cookie-authenticated data layer types. `QueryInput` is the
  wire-shaped query document (string dates / string target) plugins build by
  hand, since the generated `LLMObsQueryDSLDocumentV1Alpha1` models dates as
  `Date`.
- `PluginManifest` and friends — TypeScript mirror of the manifest JSON Schema.
- Re-exports of `@llmobs/ui` and `@llmobs/tokens` so a plugin has one import
  surface, plus the canonical `Span`/`Trace`/`QueryDocument` types.
