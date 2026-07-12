# ADR-0030: The framework-neutral plugin frontend contract (React is one binding)

- **Status:** Accepted (Arc N / N1 — make the plugin promise true)
- **Date:** 2026-07-11
- **Deciders:** m7mdhka (Arc N)
- **Amends:** **ADR-0004** (D4 — "plugins are containers + manifest; MF 2.0 frontends"),
  which pinned a plugin frontend to a **React component** loaded via Module Federation.
- **Relates to:** ADR-0023 (plugin protocol; the J1 frontend token this contract
  carries unchanged), ADR-0024 (plugin settings SchemaForm; N2 adds a custom-settings
  mount over this contract), the plugin API overview
  (`api/plugin/v1alpha1/README.md` §6/§8), the manifest schema
  (`api/schemas/manifest/v1alpha1` — unchanged, it never named React). Closes the #1
  flexibility wall from the plugin-viability audit (#66).

## Context

We advertise "any language, plug-and-play." It is **true for backends** (a plugin
backend is an HTTP service in any language behind the double-token proxy) and **false
for frontends**: ADR-0004 required a plugin's Module-Federation-exposed module to
default-export a **React component**, and the shell rendered it as JSX inside a React
provider/error-boundary tree with `react`/`react-dom`/`react-router-dom` forced as
mandatory shared singletons. A Vue/Svelte/Solid/vanilla author is simply stuck. The
viability audit ranked this the single biggest say-vs-do gap in the project.

The lock-in is **narrow and entirely shell-side**, which is why it is fixable without
touching the kernel:

- The kernel/registry/manifest are **already framework-neutral** — they transport only
  `remoteName` / `exposedModule` / `entry` / `nav`, never the word "React".
- The data layer is **already framework-neutral** — `DataClient` and the
  `kv`/`secrets`/`store`/`events`/`ingest` clients are plain `fetch` classes. The G1
  frontend-token enforcement (fail-closed: stamp the `X-LLMObs-Plugin-Frontend` marker,
  require a token, throw otherwise) lives in `DataClient.headers()`, not in any React
  code.
- React coupling lives in exactly four spots: the shell's remote loader (expects a
  `ComponentType`), the shell's `PluginRoute` (renders JSX), the SDK's
  provider/hooks, and each plugin's `export default` React component — plus the MF
  singleton config and its build-time gate.

## Decision

A plugin frontend exports a **framework-neutral mount contract** and React becomes
**one binding over it**, not the binding.

### R1 — The mount contract (normative)

A plugin's MF-exposed module MUST export:

```ts
mount(element: HTMLElement, context: PluginMountContext): PluginUnmount | void
```

The shell calls `mount` with an owned container element and a plain `context` object;
the plugin renders however it likes and returns an `unmount()` the shell calls on route
change / disable. The contract types live in the **neutral** SDK core
(`@llmobs/plugin-sdk`, `mount.ts`) and import no React. The legacy
default-React-component export is **no longer supported** — this is a breaking change to
the `v1alpha1` frontend contract, taken while the SDK is `0.x` and no third-party
frontend plugin exists yet.

### R2 — The context is a plain object (normative)

`PluginMountContext` carries: `baseUrl`, `project`, `user`, the **token-confined
`client`** (`LLMObsClient`), the `frontendToken` provider, `theme` (mode; CSS custom
properties remain global on the document root), and `locale` + `direction`. Locale and
direction are threaded **now** (N1) even though N3 wires their real values — reopening
the frontend contract twice is the expensive path.

### R3 — The G1 boundary is framework-independent (normative)

The frontend token (ADR-0023 J1) is unchanged. Because the enforcement is structural in
`DataClient` — not in a React hook — a **non-React** caller reaching `context.client` is
confined to `plugin-grant ∩ session ∩ project` identically, and fails closed if no token
can be minted. The neutral contract does not widen the boundary; the same-origin
trust model (frontend plugins are trusted-at-install; hard confinement = ship a backend;
origin isolation stays the deferred real boundary) is untouched.

### R4 — React is a shipped binding (`@llmobs/plugin-sdk/react`)

The existing hooks (`useTraces`, …) and `LLMObsPluginProvider` move to a
`@llmobs/plugin-sdk/react` subpath and become a **thin adapter** over the neutral
primitives. `createReactBinding(RootComponent)` returns a `mount` function (creates a
React root, wraps the component in the provider seeded from `context`, returns
`root.unmount`). MF singleton-pinning of `react`/`react-dom`/`react-router-dom` applies
**only** to plugins that opt into this binding; a non-React plugin shares neither.

### R5 — Dogfood proof + falsification

`plugins/tracing` (the reference Tier-2 plugin) migrates to the neutral contract via the
React binding — any gap the migration reveals is a **contract** gap, fixed in the
contract, not worked around. And a **non-React** example plugin (vanilla TS) is the
H7-equivalent falsification of "any framework": it mounts, queries via `context.client`
(confined by its frontend token), renders, and unmounts — or the report says exactly
where the contract is still React-shaped.

## Consequences

- **The promise becomes honest.** "Any framework" is provable by a working non-React
  plugin. A Vue/Svelte author scaffolds from `templates/plugin-frontend-vanilla` and
  ships without importing React.
- **Breaking, but contained.** Every existing plugin (only `tracing` + the templates)
  migrates in this arc. The manifest schema, the Go registry, and the kernel are
  untouched — no codegen, no wire change.
- **Module Federation stays the loader.** MF still resolves and loads remotes; what a
  remote must *export* changes from a React component to `mount`/`unmount`. The
  singleton list becomes React-binding-scoped.
- **N2/N3 ride this contract.** Custom settings views (N2) and locale/RTL (N3) mount and
  read through the same `context`, so the frontend contract is opened once.

## Alternatives considered

- **Keep React, document the wall.** Rejected: it is the #1 flexibility gap and the
  audit ruled it fixable; documenting a self-imposed wall we can remove is dishonest.
- **Web Components as the contract.** Rejected: a custom-element boundary adds a
  serialization seam and Shadow-DOM styling friction for no gain over a plain
  `mount(element, context)`; the neutral function is the smaller, more universal shape
  (and is the same seam the future cross-origin iframe + postMessage boundary will use).
- **A manifest `framework` field.** Rejected as unnecessary: the shell calls `mount`
  the same way regardless of framework; the kernel never needs to know. Kept additive if
  a future need (e.g. per-framework loading hints) appears.
