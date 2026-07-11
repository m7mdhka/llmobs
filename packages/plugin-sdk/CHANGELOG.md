# @llmobs/plugin-sdk changelog

Semver-sacred. The plugin SDK is the Tier-2 public surface; breaking changes
require a major bump and a deprecation note.

## 0.2.0 — framework-neutral frontend contract (Arc N / N1, ADR-0030)

**Breaking.** React is now ONE binding, not THE binding. The package root
(`@llmobs/plugin-sdk`) is framework-neutral (imports no React); the React hooks,
provider, and design system move to `@llmobs/plugin-sdk/react`.

- **New (neutral root):** the mount contract — `PluginMount`, `PluginUnmount`,
  `PluginModule`, `PluginMountContext`, `PluginUser`, `PluginTheme`, `TextDirection`
  (`mount.ts`). A plugin's exposed module exports `mount(element, context)` and returns
  `unmount()`; it may render with any framework. `DataClient`, the `kv`/`secrets`/
  `store`/`events`/`ingest` clients, the manifest + entity types, and `@llmobs/tokens`
  stay in the neutral root (they were already React-free).
- **Moved to `@llmobs/plugin-sdk/react`:** `LLMObsPluginProvider`, `useLLMObs`, the hooks
  (`useQuery`/`useTraces`/`useTrace`/`useWriteScore`/`useSettings`), `SchemaForm`, and the
  `@llmobs/ui` re-export. **New here:** `createReactBinding(Root)` — turns a React
  component into a neutral `mount` (creates a React root, wraps it in the provider seeded
  from the mount context, returns `root.unmount`).
- **`PluginMountContext.basePath`** — the shell-supplied URL base for a plugin that does
  its own routing (a React plugin passes it to `<BrowserRouter basename>`). Required
  because a neutral surface renders in its own root and cannot inherit the shell's router.
- **`react`/`react-dom` are now OPTIONAL peer deps** — a non-React plugin installs
  neither. The G1 frontend-token enforcement is unchanged (it lives in `DataClient`, so
  it confines a non-React caller identically).

Migration: a React plugin changes `export default MyComponent` →
`export const mount = createReactBinding(MyComponent)` and imports hooks/UI from
`@llmobs/plugin-sdk/react`.

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
