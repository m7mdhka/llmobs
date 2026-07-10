# The plugin inner loop — `make dev` (create → dev → edit → reload)

`make dev` is the one command that gives a plugin author a live, hot-reloading stack:
the kernel, the web shell, and a plugin — all wired, against seeded Postgres. Edit a
plugin's frontend and the browser updates without a rebuild; edit its settings schema
or backend and the change is one restart away. This is the loop the DX audit (B10)
said was missing; J3 builds it.

## What it starts

```
make dev
```

brings up, with cleanup on Ctrl-C:

| Process | Where | Reloads on save? |
|---|---|---|
| Postgres | container (`deploy/compose/dev.yaml`, published to the host) | n/a |
| Kernel (`llmobsd`) | `go run` on the host | restart (Ctrl-C + rerun — a dev restart is not a fault) |
| Web shell | rspack dev server, `:3000`, HMR | **yes** (hot) |
| Tracing plugin frontend | rspack dev server, `:3001`, HMR | **yes** (hot) |

Open **http://localhost:3000** and sign in with the dev admin (`admin@example.com` /
`admin-dev-password`).

## How the frontend hot-reloads

A plugin frontend is a Module Federation *remote*. In production the kernel serves the
built `remoteEntry.js` from the plugin's `dist/`. In dev, the plugin runs its **own**
rspack dev server (`:3001`) serving `remoteEntry.js` with HMR, and the kernel's
registry is told to advertise **that** URL instead of the built dist:

```
LLMOBS_DEV_PLUGIN_REMOTES="llmobs/tracing=http://localhost:3001/remoteEntry.js"
```

So the shell loads the plugin from its live dev server. Editing
`plugins/tracing/frontend/src/*` recompiles and hot-updates in the browser — **no
rebuild, no kernel restart, no page reload of the shell**. The integrity hash is
dropped for dev remotes (the bundle changes every save). The shell dev server proxies
the kernel-owned paths (`/auth`, `/v1alpha1`, `/api`) to the kernel so the session
cookie and all data calls work same-origin.

## How the backend reloads

The tracing plugin is frontend-only, so its inner loop is pure frontend HMR. For a
plugin **backend**, the H2 ruling applies: a restart during development is a
*dev-restart-not-a-fault*, not a health failure. Restart the backend process and the
supervisor re-handshakes it (re-issues the service token, returns it to `running`) —
you do not restart the kernel. Kernel changes themselves are a `Ctrl-C` + `make dev`
away.

## The create → dev → edit → reload walkthrough

1. **create** — `llmobs plugin create you/yourplugin` scaffolds a plugin
   (manifest + backend + tests) from `templates/` (see
   [dx-audit.md](dx-audit.md)). A frontend-only plugin scaffolds from
   `templates/plugin-frontend-only`.
2. **dev** — `make dev`. The stack comes up; your plugin appears in the shell nav
   (once its manifest + dev remote are wired, mirroring `plugins/tracing`).
3. **edit** — change a component under `frontend/src/`. Save.
4. **reload** — the browser hot-updates in place. Change the `settingsSchema` or a
   backend handler → restart that one process; the kernel/shell keep running.

## Testing your hooks without a kernel

You do not need `make dev` running to unit-test a plugin surface. The SDK ships a test
harness — see [testing your plugin](settings.md) and the SDK's
`@llmobs/plugin-sdk/testing` (`createFakeClient` + `TestLLMObsProvider`). Render your
surface against a fake client, assert what it shows and what it called:

```tsx
import { createFakeClient, TestLLMObsProvider } from "@llmobs/plugin-sdk/testing";

const client = createFakeClient({ traces: [{ trace_id: "t1" }] });
render(<TestLLMObsProvider client={client}><MyTab /></TestLLMObsProvider>);
// assert on the DOM, then inspect client.calls.query / client.calls.setSettings / …
```

## Honest limits (what J3 does and doesn't cover)

- **Verified in CI:** the dev-remote override (registry unit test), the SDK test
  utilities (they run the real hooks against a jsdom DOM — 4 self-tests), and every
  build/typecheck the stack depends on.
- **Not automated:** the end-to-end *browser* hot-reload experience (kernel + two dev
  servers + a live edit) is a human loop, validated by running `make dev`, not by a
  headless test. The pieces that make it work are each covered; the felt experience is
  a manual `make dev`.
- **One first-party plugin is wired for `make dev`** (`llmobs/tracing`). A second
  plugin joins by adding its id+dev-URL to `LLMOBS_DEV_PLUGIN_REMOTES` and starting its
  own dev server — the mechanism is general, the `dev.sh` orchestration currently lists
  one.
