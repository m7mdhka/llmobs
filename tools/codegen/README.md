# tools/codegen

Turns the contracts in `api/` into generated code, backing `make generate`:

- `api/openapi/**` → Go types (`kernel/pkg/model`) and the TS query client
  (`packages/query-client`).
- `api/schemas/**` → typed representations of the manifest, events, and settings
  schemas.

Generated output is committed and checked in CI (regenerate-and-diff). Never
hand-edit generated code — change the contract and rerun `make generate`.
