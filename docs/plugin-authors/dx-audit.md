# Plugin DX audit (H8) — honest, against the B10 findings

The B10 flexibility story was a plugin author's *diary* — the cries-and-quits
moments. This is where the Tier-3 DX actually stands against it: what closed, and
what is still a gap. Written to find the gaps, not to claim the loop is done.

## What closed

- **Scaffold — `llmobs plugin create <owner/name>`.** A real CLI command
  (`cli/`, stdlib-only) generates a Python plugin from a built-in template
  born-identical to `templates/plugin-python` — handshake + health implemented,
  the protocol library included, the manifest templated. Zero → a running-shaped
  plugin in one command. (Closes "no scaffold; copy-paste and guess.")
- **Offline contract check.** The scaffold ships `interop_test.py`: a plugin author
  verifies, with no kernel running, that they can validate a kernel-signed
  assertion (the language-agnostic proof). Fast feedback before any integration.
- **Conformance harness** (`internal/pluginconform`, run in CI over every
  first-party manifest): manifest validity, capability-within-grant, handshake
  correctness, health shape. First-party plugins meet the bar they ask third
  parties to meet — the future "verified plugin" gate.
- **A real proving plugin to copy.** `plugins/langfuse-compat` is a complete,
  CI-exercised backend (handshake → token delivery → ingest → visible trace) — a
  worked example, not just docs.

## What is still a gap (the honest cries-and-quits list)

- **No `make dev` hot-reload loop.** There is no one-command "kernel + shell + my
  plugin, reloading on save, against seeded data." An author still wires up
  compose + the loadgen by hand. This is the single biggest remaining DX gap — the
  inner loop is create-then-figure-it-out, not create-then-iterate.
- **No frontend-plugin dev path for persistence.** A *pure-frontend* plugin cannot
  persist config/settings without a backend, because the frontend-direct plugin
  identity (a shell-minted, per-plugin assertion) does not exist yet — kv/secrets/
  store are backend-double-token only (the deliberate H4 deferral). So B4/B9-style
  frontend plugins must stand up a backend just to save a layout. See the Set-B
  re-color.
- **SchemaForm is not built.** The manifest declares `settingsSchema`, but the
  `packages/schema-form` renderer that would turn it into a settings tab is still
  designed-not-built. A settings UX is therefore hand-rolled.
- **No SDK test utilities for hooks.** There is no provided harness to unit-test a
  plugin frontend's `useQuery`/`useWriteScore` against a fake client; authors mock
  by hand.
- **CLI is `create`-only.** `init/dev/apply/render/bundle/backup` remain
  skeletons; `plugin create` is the one real command this arc.

## Priority read

The primitives are real and a backend plugin is genuinely buildable today
(see the Set-B re-color: B3/B6/B7/B8/B10 are first-class). The DX gap is now the
binding constraint, not the platform — and within DX, the two highest-leverage
items are **`make dev` hot-reload** (fixes the inner loop for everyone) and the
**frontend-direct plugin identity** (unblocks the pure-frontend persistence
stories B2/B4/B9). Both are post-Tier-3 roadmap, named here rather than glossed.
