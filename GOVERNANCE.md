# Governance

This document describes how the LLMObs project is governed. It is intentionally
lightweight for the project's current stage and will formalize as the community
grows.

## Principles

- **Vendor-neutral and fully open.** The core is Apache-2.0 and never
  feature-gated. A commercial hosted offering may exist later, but it builds on
  the same public plugin API everyone else uses.
- **Contracts before code.** Architectural direction is captured in
  Architecture Decision Records ([`docs/adr/`](docs/adr/)) and in the versioned
  contracts under [`api/`](api/).
- **Dogfooding.** First-party features are plugins built on the public API. The
  maintainers experience the platform the way plugin authors do.

## Roles

### Users
Anyone who runs LLMObs. Feedback via issues and discussions is the primary
input to the roadmap.

### Contributors
Anyone who submits a pull request, files an issue, or improves docs. All
contributions require a DCO sign-off (see [`CONTRIBUTING.md`](CONTRIBUTING.md)).

### Maintainers
Contributors with merge rights. Maintainers:

- Review and merge pull requests.
- Create `release/*` and `hotfix/*` branches and cut releases.
- Steward the architecture invariants and contract compatibility.
- Are listed in [`.github/CODEOWNERS`](.github/CODEOWNERS).

New maintainers are proposed by an existing maintainer and confirmed by
maintainer consensus, based on a sustained track record of quality
contributions and good judgment.

## Decision making

- **Day-to-day changes:** lazy consensus. A PR that satisfies review and CI may
  be merged.
- **Architectural decisions** (new hot-path dependency, new public API, storage
  schema change, new top-level directory): require an ADR in the same PR and
  approval from at least one maintainer who is a code owner of the affected
  area.
- **Contract breaking changes:** require an ADR plus a deprecation window per
  [`VERSIONING.md`](VERSIONING.md).
- **Disputes:** resolved by maintainer discussion seeking consensus; if
  consensus cannot be reached, a simple majority of maintainers decides.

## Changing this document

Amendments to `GOVERNANCE.md` follow the architectural-decision process: an ADR
plus maintainer consensus.
