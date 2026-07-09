# LLMObs

**"Grafana for LLM observability."** An open-source (Apache-2.0), plugin-based
LLM observability platform: a small kernel plus a real plugin ecosystem.

Existing tools (Langfuse, Opik, Phoenix) are monoliths — you get every feature
whether you want it or not, and adding a custom tab means forking the codebase.
LLMObs is a **microkernel** plus plugins: tracing, evaluation, and prompt
management are plugins that install, uninstall, and version exactly like
community plugins. A Python developer can ship a custom tab as a container +
manifest without ever reading kernel code.

The commercial model is **fully-OSS core + closed-source hosted cloud later**;
nothing in this repo is ever feature-gated.

> **Status:** Foundational scaffolding. Contracts and structure are being laid
> down before implementation. See [`docs/adr/`](docs/adr/) for the decision
> history and [`CLAUDE.md`](CLAUDE.md) for the architecture invariants.

## Architecture at a glance

The kernel owns only: OTLP ingestion + dialect normalizers, storage adapters,
auth/tenancy, plugin registry + supervisor, event bus, a typed Query API, and
jobs/kv/secrets. Everything else — including all first-party features — is a
plugin built on the same public plugin API third parties use (the **dogfood
rule**).

Two deployment profiles from the same codebase:

- **Lite** — Postgres-only, `docker compose up`, shared first-party plugin runtime.
- **Scale** — ClickHouse + queue + S3, Helm, isolated plugin containers.

## Repository layout

| Path | Purpose |
|---|---|
| [`api/`](api/) | ★ Source of truth: OpenAPI specs, JSON Schemas, canonical model. Contracts change first. |
| [`kernel/`](kernel/) | Go kernel. `internal/` is private; `pkg/` is the public plugin-facing surface. |
| [`cli/`](cli/) | The `llmobs` CLI. |
| [`web/shell/`](web/shell/) | Module Federation 2.0 host app (Rspack). |
| [`packages/`](packages/) | TypeScript workspace: `plugin-sdk`, `ui`, `tokens`, `query-client`, `schema-form`, `brand`. |
| [`plugins/`](plugins/) | First-party plugins, structured exactly like third-party ones. |
| [`runtimes/`](runtimes/) | Shared first-party plugin runtime host (lite profile). |
| [`deploy/`](deploy/) | compose, Helm, K8s operator, air-gap bundles. |
| [`templates/`](templates/) | Plugin scaffolds used by `llmobs plugin create`. |
| [`tools/`](tools/) | loadgen, codegen, conformance harness. |
| [`docs/`](docs/) | Docs site source + Architecture Decision Records. |

## Getting started

```sh
make setup   # install toolchain deps, pre-commit hooks, git-flow init
make dev     # run the lite profile locally
make test    # unit tests
make lint    # linters
make e2e     # end-to-end tests
```

Toolchain versions are pinned in [`mise.toml`](mise.toml). The
[`.devcontainer/`](.devcontainer/) provides the full stack in one container.

## Contributing

Read [`CONTRIBUTING.md`](CONTRIBUTING.md). All commits require a
[DCO](https://developercertificate.org/) sign-off (`git commit -s`) and follow
[Conventional Commits](https://www.conventionalcommits.org/) via Commitizen.

## Governance & versioning

- [`GOVERNANCE.md`](GOVERNANCE.md) — how decisions are made.
- [`VERSIONING.md`](VERSIONING.md) — the version-train + independent-SDK policy.
- [`SECURITY.md`](SECURITY.md) — vulnerability disclosure.
- [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) — community standards.

## License

Apache-2.0. See [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).
