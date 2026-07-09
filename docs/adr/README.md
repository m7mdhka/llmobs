# Architecture Decision Records

Numbered, immutable-once-accepted records of the architectural decisions behind
LLMObs. Use the `new-adr` skill to add one. An ADR is required in the same PR
for: a new hot-path dependency, a new public API, a storage schema change, a new
top-level directory, or breaking a published contract.

## Index

ADR-0001..0015 backfill the locked design decisions (the "D-table") so the
decision history exists from the start:

| ADR | Decision |
|---|---|
| ADR-0001 | Microkernel architecture (D1) |
| ADR-0002 | Dogfood rule — first-party plugins use only the public API (D2) |
| ADR-0003 | OTLP-canonical ingestion; normalizers vs. compat plugins (D3) |
| ADR-0004 | Plugins are containers + manifest; MF 2.0 frontends (D4) |
| ADR-0005 | Double-token auth; permission intersection (D5) |
| ADR-0006 | Declarative supervisor, pluggable executors (D6) |
| ADR-0007 | Two deployment profiles: lite and scale (D7) |
| ADR-0008 | Seven SDK primitives; capabilities are data nouns (D8) |
| ADR-0009 | Typed query DSL, compiled per adapter — no raw SQL (D9) |
| ADR-0010 | Ingestion middleware chain; redaction before persistence (D10) |
| ADR-0011 | K8s-style API versioning; additive-only within a major (D11) |
| ADR-0012 | GitHub-native plugin distribution; namespaced IDs (D12) |
| ADR-0013 | Performance targets for lite and scale (D13) |
| ADR-0014 | Governance: DCO, Apache-2.0, air-gap as first-class (D14) |
| ADR-0015 | Rename stays cheap — brand in one module per language (D15) |

ADR-0001..0015 are backfilled during bootstrap (see the design doc's bootstrap
order). New decisions continue the numbering:

| ADR | Decision |
|---|---|
| [ADR-0016](0016-canonical-data-model-v1alpha1.md) | Canonical data model `v1alpha1` (master; LM-1..LM-12) |
| [ADR-0017](0017-score-model.md) | Score model (LM-3 + LM-8) |
| [ADR-0018](0018-span-taxonomy-and-payload-shapes.md) | Span taxonomy and payload shapes (LM-1 + LM-2) |
| [ADR-0019](0019-query-dsl.md) | Query DSL (QD-1..QD-10) |
