# Vanilla (framework-free) plugin frontend template

A Tier-2 LLMObs plugin frontend with **no framework** — no React, no Vue, no Svelte,
just the neutral mount contract (ADR-0030) and plain DOM. It is the living proof that
`@llmobs/plugin-sdk` is framework-agnostic: the shell mounts and drives it with zero
React in scope.

## The contract

Your exposed module exports a single function:

```ts
import type { PluginMount } from "@llmobs/plugin-sdk";

export const mount: PluginMount = (element, context) => {
  // render into `element` with whatever you like (here: document APIs)
  // query data through the token-confined client:
  //   context.client.query({ target: "traces", timeRange: {…}, limit: 20 })
  // read context.basePath (routing), context.theme, context.locale/direction
  return () => {
    /* tear everything down: remove DOM, abort requests, clear timers */
  };
};
```

The shell calls `mount(element, context)` and calls the returned `unmount()` on route
change. See `src/plugin.ts` for a complete example that queries recent traces and renders
them, and `src/plugin.test.ts` for the falsification test that drives it with no React.

## What's different from the React template

- **Dependencies:** only `@llmobs/plugin-sdk` (neutral core) + `@llmobs/tokens` (CSS
  custom properties). No `react`, `react-dom`, `react-router-dom`, or `@llmobs/ui`.
- **Shared singletons** (`rspack.config.mjs`): only the neutral SDK + tokens. A React
  plugin additionally shares the React singletons; you don't.
- **Routing:** you own it. `context.basePath` is your URL base — wire it into your own
  router (or keep a single view, as the example does).

## The frontend token still confines you (G1)

`context.client` carries the plugin frontend token and **fails closed**: a call for which
no token can be minted throws rather than running at the user's full session scope. This
enforcement is in the data client, so it is identical for a non-React caller — see the
`PROVE-THE-NEGATIVE` case in `src/plugin.test.ts`. The same-origin trust model is
unchanged (a frontend plugin is trusted-at-install; hard confinement means shipping a
backend). See `docs/plugin-authors/trust-model.md`.

## Prefer React?

Use `templates/plugin-frontend-only` (the React binding) — the hooks (`useTraces`, …) and
the design system (`@llmobs/ui`) are a thin adapter over this same neutral contract.
