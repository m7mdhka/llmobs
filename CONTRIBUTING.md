# Contributing to LLMObs

Thank you for contributing. This project is Apache-2.0 and intends to remain a
healthy, vendor-neutral open-source community. Please read this document and
[`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) before opening a pull request.

## Developer Certificate of Origin (DCO) — required

Every commit must be signed off under the
[Developer Certificate of Origin](https://developercertificate.org/). This is a
lightweight statement that you have the right to submit the contribution.

Sign off by adding a `Signed-off-by` trailer to each commit:

```sh
git commit -s -m "feat(kernel): add openinference normalizer"
# or, when authoring with Commitizen:
cz commit --signoff
```

The DCO GitHub App enforces sign-off on every PR. Commits without a valid
`Signed-off-by` line matching the commit author will fail CI.

## Development environment

The fastest path is the devcontainer:

- Open the repo in VS Code and "Reopen in Container", or use the GitHub
  Codespaces button. The container has the full toolchain.
- Outside a container, install [mise](https://mise.jdx.dev/), then run
  `mise install` to get pinned tool versions from [`mise.toml`](mise.toml).

Then:

```sh
make setup   # deps, pre-commit hooks, git-flow init
```

Everything runs through `make`: `setup`, `dev`, `build`, `test`, `lint`, `e2e`,
`generate`. If a task has no make target, add one rather than documenting a raw
command.

## Branching — gitflow

We use gitflow (AVH edition).

| Branch | Purpose |
|---|---|
| `main` | Released code only. Every merge is a tagged train release. |
| `develop` | Integration branch and the **default PR target**. |
| `feature/<scope>-<desc>` | All work. Branch from `develop`. |
| `release/vX.Y.0` | Stabilization. Maintainers only. |
| `hotfix/vX.Y.Z` | Critical fixes from `main`. Maintainers only. |

- Branch from `develop` as `feature/<scope>-<short-desc>`.
- **Never commit to `develop` or `main` directly.**
- Keep feature branches short-lived (days, not weeks); merge behind config
  flags rather than maintaining long-lived branches.
- Features are **squash-merged** into `develop` (one conventional commit per PR).

## Commits — Conventional Commits via Commitizen

Author commits with `cz commit --signoff` (or `git commit -s` following the
convention). Format:

```
type(scope): short imperative summary
```

- **Allowed scopes** (the top-level directories): `kernel`, `cli`, `web`,
  `sdk`, `ui`, `plugins`, `api`, `deploy`, `docs`, `tools`, `ci`, `repo`.
- Keep commits atomic — one logical change each.
- `cz check` runs in pre-commit and CI and on the PR title.

## Pre-commit hooks are law

Pre-commit runs Commitizen, gofmt/goimports, golangci-lint, eslint/prettier,
gitleaks, and license-header checks. **Never use `--no-verify`.** If a hook
fails, fix the cause.

## Definition of done

Before you mark a PR ready:

1. `make lint && make test` pass.
2. Contract changes (`api/`) have regenerated code committed (`make generate`).
3. New behavior has tests.
4. Fixture changes are justified in the PR description.
5. User-facing changes update the relevant docs.
6. Architectural decisions include an ADR in [`docs/adr/`](docs/adr/) in the
   same PR.

### For plugin-facing changes specifically

1. `api/` updated + codegen run.
2. Implementation in kernel + `@llmobs/plugin-sdk` support.
3. At least one first-party plugin exercises it.
4. The conformance harness (`tools/conformance`) covers it.
5. `docs/plugin-authors` updated.

## Dependency licensing

Only **Apache-2.0 / MIT / BSD** dependencies are permitted. **AGPL and SSPL are
prohibited.** Run the license check when adding a dependency.

## Security

Do not open public issues for vulnerabilities. Follow the process in
[`SECURITY.md`](SECURITY.md).
