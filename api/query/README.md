# api/query/ — the Query API contract

The **Query API** is the single, typed read path over the canonical data model
(`api/model/`). It is the only way a plugin reads data — there is **no raw-SQL
escape hatch** (design decision D9, invariants 2–3). The contract is compiled per
storage adapter (Postgres for lite, ClickHouse for scale) and MUST return
identical results from both.

| Path | Purpose |
|---|---|
| `v1alpha1/00-dsl-spec.md` | Normative spec of the query DSL: shape, field classes & operators, boolean/nesting rules, aggregations, bounded scans, pagination, cost ceilings, response envelope, score semi-join, permissions & field-level redaction, error taxonomy. |
| `v1alpha1/dsl.schema.json` | JSON Schema (Draft 2020-12) for a query document — encodes the structure and ceilings so an invalid query is rejected before it reaches an adapter. |
| `v1alpha1/fields.json` | The queryable field registry: every promoted field per target with its class, orderability, group-ability, and model-spec anchor. Hand-authored now; to be generated from the model spec later (a CI check asserts it matches `api/model/`). |
| `v1alpha1/examples/` | Example queries (valid + deliberately invalid), validated against `dsl.schema.json` in CI. |

The HTTP surface (the `POST /v1alpha1/query` endpoint, the tree/single-entity
fetches, and the score write path) is defined in
`api/openapi/v1alpha1/query.yaml`, which `$ref`s the model JSON Schemas in
`api/model/v1alpha1/schema/` — one source of truth for entity shapes.

Ingestion (OTLP) is a **different surface** and is not part of this contract.

Maturity/versioning per `VERSIONING.md`: `v1alpha1`, additive-only within a
major. See ADR-0019.
