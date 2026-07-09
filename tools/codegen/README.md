# tools/codegen

Turns the contracts in [`api/`](../../api/) into generated code. Backs
`make generate`. **Generated output is committed and checked in CI**
(regenerate-and-diff): a drift or hand-edit fails the build. Never hand-edit a
generated file — change the contract and rerun `make generate`.

## Pipeline (`generate.sh`)

| Input (source of truth) | Generator | Output (committed) |
|---|---|---|
| `api/model/v1alpha1/schema/*.schema.json` | quicktype | `kernel/pkg/model/model_gen.go` (Go types) |
| `api/model/…` + `api/query/v1alpha1/dsl.schema.json` | quicktype | `packages/query-client/src/types.gen.ts` (TS types) |
| `api/openapi/v1alpha1/query.yaml` | bundle → oapi-codegen | `kernel/internal/gateway/queryapi/server_gen.go` (Go server interface) |

Hand-written companions (NOT generated): `packages/query-client/src/client.ts`
(thin fetch wrapper over the OpenAPI operations) and `src/index.ts`.

## Generator choices (and why)

- **[quicktype](https://quicktype.io) `23.0.171`** (pinned in `package.json`) for
  JSON Schema → Go **and** TypeScript types. One boring, widely-used tool for both
  languages, and — critically — it handles JSON Schema **2020-12** (boolean
  schemas, `$ref` across files, `if/then`, `const`) which our model/DSL schemas
  use. Types are generated straight from the single source of truth.
- **[oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) `v2.4.1`** (run via
  `go run`, version pinned in `generate.sh`) for OpenAPI → Go server interface +
  routing. Single Go binary, well maintained, standard-library server output
  (`std-http-server`).
- **`yaml` `2.5.1`** (pinned) — used by `bundle_openapi.mjs`.

### Why the OpenAPI is bundled first

`api/openapi/v1alpha1/query.yaml` `$ref`s the full JSON Schema 2020-12 model
schemas as the single source of truth. oapi-codegen's loader (kin-openapi) does
**not** accept 2020-12 constructs. `bundle_openapi.mjs` therefore produces an
oapi-codegen-loadable OpenAPI **3.0.3** bundle used *only* to generate the server
interface: every external `$ref` is replaced with an **opaque** object (the rich
entity/DSL types come from quicktype and are validated at runtime against the
original JSON Schemas), `const` is lowered to `enum`, and JSON-Schema-only
keywords are dropped. The bundle (`.openapi-bundle.json`) is a build artifact and
is git-ignored.

## Toolchain note — Go 1.24

oapi-codegen `v2.4.1`'s `runtime` dependency (`v1.4.2`, used for path-parameter
binding) requires **Go ≥ 1.24**. The repo Go pin was therefore bumped
`1.23 → 1.24` (`mise.toml`, `go.work`, `kernel/go.mod`, `cli/go.mod`). Go 1.24 is
a current stable release; this is a deliberate toolchain decision to use a
maintained OpenAPI generator.

## Running

```sh
pnpm --dir tools/codegen install --ignore-workspace   # once (also done by `make setup`)
make generate                                         # regenerate everything
```

`make generate` is **idempotent**: identical contracts + pinned generators →
byte-identical output. CI runs it and diffs the generated paths.
