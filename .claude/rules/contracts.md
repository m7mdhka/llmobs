---
scope: ["api/**"]
---

# Contracts (`api/` is the source of truth)

`api/` depends on nothing; everything depends on `api/`. Contracts change first,
code second — always.

- **Additive-only within a released maturity version.** In `v1alpha1`/`v1beta1`/
  `v1` you may add optional fields; you may not remove, rename, or repurpose
  existing ones. Breaking a published contract requires an ADR + deprecation
  window.
- **Maturity promotion checklist** (`v1alpha1 → v1beta1 → v1`): fields stable,
  covered by fixtures/conformance, documented in `docs/`, compatibility matrix
  updated, ADR recorded. Old maturity dirs are retained until formally removed.
- **Run `make generate` after any edit.** Generated Go (`pkg/model`) and TS
  (`packages/query-client`) must be regenerated and committed in the same PR.
  CI runs a regenerate-and-diff check; a dirty diff fails the build.
- **Never hand-edit generated code.** Fix the spec/schema and regenerate.
- Applies to: Query API + control-plane OpenAPI, the plugin manifest schema,
  event bus payload schemas, and the canonical data model spec.
