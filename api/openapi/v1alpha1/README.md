# OpenAPI — v1alpha1

OpenAPI 3.1 specs for the public HTTP contracts, at the `v1alpha1` maturity
level. Three specs live here:

- **Query API** — the typed query surface plugins call to read data. Filter /
  aggregate DSL grammar expressed as request bodies; no raw SQL. Permission is
  the intersection of the plugin service token and the forwarded user assertion.
- **Control-plane API** — plugin registry, supervisor desired-state + health,
  auth/session/token endpoints, audit.
- **Plugin protocol** — the kernel↔plugin handshake, capability grants, surface
  registration.

These specs feed `tools/codegen`, which generates `kernel/pkg/model` (Go) and
`packages/query-client` (TS). Edit the spec, then `make generate` — never
hand-edit the generated clients.

Additive-only at this maturity level. Promotion to `v1beta1` follows the
checklist in [`.claude/rules/contracts.md`](../../../.claude/rules/contracts.md).
