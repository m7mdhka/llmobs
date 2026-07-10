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

- **`make dev` hot-reload loop — CLOSED (J3).** One command brings up the kernel +
  shell + a plugin, all wired against seeded Postgres, with the frontend hot-reloading
  on save (the plugin runs its own rspack dev server; the kernel's dev-remote override
  points the registry at it). A backend restart re-handshakes (dev-restart-not-a-fault,
  H2). See [dev-loop.md](dev-loop.md). The inner loop is now create-then-iterate.
- **Frontend-direct plugin identity — CLOSED (J1).** A *pure-frontend* plugin now
  has a confined-by-default identity: the shell mints a per-plugin, per-session,
  short-TTL **frontend token** (`plugin-grant ∩ session ∩ project`) and the SDK data
  hooks present it transparently. A frontend plugin's Query API calls run at
  least-privilege without standing up a backend. **Honest caveat:** this is
  least-privilege *by default*, not a boundary — a frontend shares the shell's origin
  and can bypass the SDK with the ambient cookie, so a frontend-only plugin is
  *trusted-at-install* (see [trust-model.md](trust-model.md)); untrusted logic still
  belongs in a backend. This unblocks B2/B4/B9-style frontend persistence (J2 builds
  the kv-backed settings store on top of this identity).
- **SchemaForm — CLOSED (J2).** `packages/schema-form` renders a plugin's
  `settingsSchema` as a settings tab, validated client + kernel side, with `writeOnly`
  secrets that are never rendered back. See [settings.md](settings.md).
- **SDK test utilities — CLOSED (J3).** `@llmobs/plugin-sdk/testing` ships a
  `createFakeClient` + `TestLLMObsProvider` so an author unit-tests
  `useQuery`/`useTraces`/`useWriteScore`/`useSettings` against a fake client, no kernel
  running — the SDK's own hooks are self-tested this way. See [dev-loop.md](dev-loop.md).
- **CLI is `create`-only.** `init/dev/apply/render/bundle/backup` remain
  skeletons; `plugin create` is the one real command this arc.

## Priority read (post-Arc-J)

The primitives are real, a backend plugin is genuinely buildable, and — after Arc J —
the binding DX constraints the original audit named are closed: **frontend-direct
plugin identity (J1)**, **SchemaForm settings (J2)**, and the **`make dev` inner loop
+ SDK test utilities (J3)**. A pure-frontend plugin can now be created, run with hot
reload, persist settings (secrets included), and be unit-tested — without standing up
a backend or reading kernel code. The remaining gaps are the still-skeleton CLI verbs
(`init/dev/apply/render/bundle/backup`) and breadth (one first-party plugin is wired
end-to-end for `make dev`); both are named, not glossed.
