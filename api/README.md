# api/ — the source of truth

`api/` depends on nothing. **Everything depends on `api/`.** Contracts change
first, code second — always. In a 10-year project, code is rewritten; contracts
are renegotiated. This directory holds those contracts.

## Layout

| Path | What lives here |
|---|---|
| `openapi/` | OpenAPI specs: the Query API, the control-plane API, and the plugin protocol. Versioned by K8s-style maturity (`v1alpha1/`). |
| `schemas/manifest/` | JSON Schema for `llmobs-plugin.yaml` (the plugin manifest). |
| `schemas/events/` | JSON Schemas for event bus payloads. |
| `schemas/settings/` | Conventions for plugin settings-form schemas. |
| `proto/` | Reserved — if gRPC is ever added for internal transport. |
| `model/` | The canonical data model spec (markdown + schema): span / trace / score. |

## Rules

- **Additive-only within a released maturity version** (`v1alpha1` → `v1beta1`
  → `v1`). Removing/renaming/retyping an existing field is breaking and requires
  an ADR + deprecation window.
- **Run `make generate` after every edit.** Generated Go (`kernel/pkg/model`)
  and TS (`packages/query-client`) are produced from here and must be committed
  in the same PR. CI runs a regenerate-and-diff check.
- **Never hand-edit generated code.** Fix the contract and regenerate.
- Old maturity directories are retained until formally removed at the end of a
  deprecation window.

See [`.claude/rules/contracts.md`](../.claude/rules/contracts.md) and
[`VERSIONING.md`](../VERSIONING.md).
