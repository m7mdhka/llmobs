# LLMObs — Project Memory for Claude Code

## What we are building

LLMObs is an open-source (Apache-2.0), plugin-based LLM observability platform —
"Grafana for LLM observability." Existing tools (Langfuse, Opik, Phoenix) are
monoliths: users get every feature whether they want it or not, and adding a
custom tab means forking the codebase. LLMObs is a small kernel plus a real
plugin ecosystem: tracing, evaluation, and prompt management are plugins that
install, uninstall, and version exactly like community plugins. A Python
developer can ship a custom tab as a container + manifest without ever reading
kernel code. The commercial model is fully-OSS core + closed-source hosted
cloud later; nothing in this repo is ever feature-gated.

## Architecture invariants — NEVER violate these

1. **Microkernel.** Kernel owns only: OTLP ingestion + dialect normalizers,
   storage adapters, auth/tenancy, plugin registry + supervisor, event bus,
   typed Query API, jobs/kv/secrets. Features live in plugins.
2. **Dogfood rule.** First-party plugins (plugins/*) use ONLY the public plugin
   API: `pkg/pluginproto`, `pkg/model`, `@llmobs/plugin-sdk`. If you are tempted
   to import kernel internals from a plugin, the plugin API has a gap — propose
   the API change instead. CI enforces this; do not fight the lint.
3. **Plugins never touch infrastructure.** No DB connections, no Redis, no S3
   from plugin code. Everything goes through the gateway's plugin APIs.
4. **Seven SDK primitives:** query, write, events, jobs, kv, secrets, surface.
   Capabilities are nouns about data, never verbs about features. Do not add a
   kernel API named after a feature (no "evals API", no "prompts API").
5. **Contracts first.** Any change to request/response shapes, events, the
   manifest, or the canonical model starts in `api/` (OpenAPI/JSON Schema),
   then codegen (`make generate`), then implementation. Never hand-edit
   `packages/query-client` or other generated code.
6. **OTLP is canonical.** New ingestion formats = a normalizer in
   `kernel/internal/dataplane/normalize/` (hot path, pure functions, fixture-
   tested) or a compat plugin (cold path, own endpoint). Raw attributes are
   always preserved alongside canonical fields.
7. **Double-token auth.** Plugins receive kernel-signed identity assertions;
   plugin→kernel calls carry service token + user assertion; the Query API
   computes the permission intersection. Never trust a plugin-supplied
   identity. Never forward session cookies to plugins.
8. **Versioning:** plugin API / manifest / events follow K8s-style maturity
   (v1alpha1 → v1beta1 → v1). Within a major: additive-only. Breaking a
   published contract requires an ADR and a deprecation window.
9. **Deployability:** every feature must work in BOTH profiles — lite
   (Postgres-only, compose, shared plugin runtime) and scale (ClickHouse,
   Helm, isolated containers). The kernel never talks to the Docker socket;
   orchestration goes through supervisor executors.
10. **Branding:** the product name exists only in `kernel/pkg/brand` and
    `packages/brand`. Never hardcode "llmobs" elsewhere (strings, tables,
    env prefixes use the brand constant).
11. **Enforce invariants at the convergence seam, not per-caller** (ADR-0025 R6).
    Put a guard at the ONE seam every path funnels through — the persist stage, the
    Query API `auth()` intersection, the plugin-token verify — never re-checked at
    each caller. A new caller must inherit the invariant by construction, not by
    remembering to re-check it; per-entry-point guards drift and one new path forgets.
12. **Every retry/requeue path needs an explicit permanent-vs-transient failure
    taxonomy.** "Retry forever on transient failure" WITHOUT classifying permanent
    failures is a resource-exhaustion or data-loss vector: a permanently-failing
    record either loops forever pinning resources, or gets dropped to make room.
    Classify at the seam — a permanent failure (malformed input, invalid credentials,
    a deterministic rejection) dead-letters; a transient failure (backend down, a
    Redis blip) retries and is NEVER dropped. This is the general form of the bug
    class the adversarial review keeps catching: **the failure path, not the happy
    path, is where correctness mechanisms hide their worst bugs** (L3: auth-DB-down
    requeues, bad-credentials drops; the WAL spool `handle()` split).

## Repo map (where things go)

- `api/` — OpenAPI specs, JSON Schemas, canonical model spec. Source of truth.
- `kernel/` — Go kernel. `internal/` = private; `pkg/` = public plugin-facing.
- `cli/` — the `llmobs` CLI (init/add/dev/apply/render/bundle/backup/plugin).
- `web/shell/` — Module Federation 2.0 host (Rspack).
- `packages/` — TS: plugin-sdk (semver-sacred), ui, tokens, query-client
  (generated), schema-form, brand.
- `plugins/` — first-party plugins, structured exactly like third-party ones.
- `runtimes/shared/` — multi-plugin host process for lite profile.
- `deploy/` — compose, helm, operator (CRDs), airgap.
- `templates/` — plugin scaffolds used by `llmobs plugin create`.
- `tools/` — loadgen, codegen, conformance harness.
- `docs/adr/` — Architecture Decision Records. Read before proposing
  architectural changes; add one for any new architectural decision.

## Tech stack

- Kernel + CLI: Go (version pinned in mise.toml). Style: standard library
  first; small, explicit interfaces; table-driven tests; errors wrapped with
  context; no panics outside main.
- Web + SDK: TypeScript strict, React, Rspack (shell) / Vite (plugin
  templates), pnpm workspaces + Turborepo, Tailwind + Radix (clone-and-own in
  packages/ui — never add shadcn via CLI, copy into the package).
- Data: Postgres (metadata + lite traces), ClickHouse (scale traces),
  S3-compatible blob, Redis Streams (lite bus) / NATS JetStream (scale).
- Everything runs through `make`: setup, dev, build, test, lint, e2e,
  generate. If a task has no make target, add one rather than documenting a
  raw command.

## Workflow rules

- **Gitflow.** Branch from `develop` as `feature/<scope>-<short-desc>`.
  Never commit to `develop` or `main` directly. Release and hotfix branches
  are created by maintainers only.
- **Commits: Commitizen conventional commits, signed off.** Use
  `cz commit --signoff` or `git commit -s -m "type(scope): message"`.
  Allowed scopes: kernel, cli, web, sdk, ui, plugins, api, deploy, docs,
  tools, ci, repo. Keep commits atomic; one logical change each.
- **Before declaring any task done:** `make lint && make test` pass;
  contract changes have regenerated code committed; new behavior has tests;
  fixture changes are justified in the PR description; docs updated if
  user-facing.
- **Pre-commit hooks are law.** Never use `--no-verify`. If a hook fails,
  fix the cause.
- **ADRs.** Architectural decisions (new dependency on the hot path, new
  public API, storage schema change, new top-level directory) require an ADR
  in docs/adr/ in the same PR.

## Definition of done for a plugin-facing change

1. `api/` updated + codegen run.
2. Implementation in kernel + `@llmobs/plugin-sdk` support.
3. At least one first-party plugin exercises it.
4. Conformance harness (tools/conformance) covers it.
5. docs/plugin-authors updated.

## Things you must never do

- Import `kernel/internal/...` from plugins, cli, or web.
- Hand-edit generated code.
- Add a feature that works in scale profile but not lite (or vice versa).
- Weaken the permission intersection in the Query API.
- Log or persist prompt/completion payloads in tests fixtures containing
  real user data — fixtures are synthetic only.
- Bypass pre-commit, force-push shared branches, or commit secrets
  (gitleaks runs in pre-commit and CI).
- Use AGPL/SSPL-licensed dependencies. Apache-2.0/MIT/BSD only; run license
  check when adding deps.
