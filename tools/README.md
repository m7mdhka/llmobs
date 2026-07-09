# tools/

Repo tooling.

| Path | Purpose |
|---|---|
| `loadgen/` | Synthetic OTel GenAI traffic generator (D13). Doubles as the CI perf gate driver and a demo-data seeder. |
| `codegen/` | Generates Go/TS clients from `api/openapi` and types from JSON Schemas. Backs `make generate`. |
| `conformance/` | The plugin/dialect conformance test harness — the "verified" bar. Replays fixtures and validates plugins against the public contract. |
