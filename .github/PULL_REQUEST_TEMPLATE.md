<!--
PR titles must be a Conventional Commit: type(scope): summary
Allowed scopes: kernel, cli, web, sdk, ui, plugins, api, deploy, docs, tools, ci, repo
-->

## What & why

<!-- What does this change do, and why? Link issues with "Closes #NNN". -->

## Type of change

- [ ] Feature
- [ ] Fix
- [ ] Refactor / chore
- [ ] Docs
- [ ] Contract change (`api/`)

## Definition of done

- [ ] `make lint && make test` pass
- [ ] Contract changes: `api/` updated **and** `make generate` output committed
- [ ] New behavior has tests
- [ ] Fixture changes justified below (synthetic data only)
- [ ] User-facing changes update docs
- [ ] Architectural decision recorded as an ADR in `docs/adr/` (if applicable)

### For plugin-facing changes

- [ ] `api/` updated + codegen run
- [ ] Kernel + `@llmobs/plugin-sdk` support implemented
- [ ] At least one first-party plugin exercises it
- [ ] `tools/conformance` covers it
- [ ] `docs/plugin-authors` updated

## Fixture / breaking-change notes

<!-- Justify any fixture changes. Note any breaking contract changes + the ADR. -->

## Sign-off

- [ ] All commits are signed off (DCO): `git commit -s` / `cz commit --signoff`
