# ADR-0020: Indexed attribute keys (per-project custom dimensions)

- **Status:** Accepted (implementation scheduled post-Tier-3)
- **Date:** 2026-07-10
- **Deciders:** m7mdhka (flexibility-audit remediation)
- **Relates to:** ADR-0016 (canonical model), ADR-0019 (query DSL). Audit Stories
  2 (Tariq), 18 (Grace).

## Context

The flexibility audit found four developers (Stories 1, 2, 5, 14) pressing on the
same wall: the **closed canonical contract** — a frozen `kind` enum and a
**permanent promoted-field set**. The resistance is intentional: a small, closed
promoted set is what keeps the model portable across adapters and its queries
predictable. But Tariq (Story 2) has a per-tenant dimension (`customer_ref`) that
is his single hottest filter, and promoting it into the canonical model is both a
one-way door for the whole product and wrong (it is *his* dimension, not
everyone's). Attributes-map filtering serves him functionally today but is
neither orderable nor first-class-indexed.

We want a mechanism that gives a **deployment** first-class, indexed, orderable
custom dimensions **without touching the canonical model**.

## Decision

Introduce **indexed attribute keys**: a per-project configuration that promotes a
chosen set of `attributes` keys to an indexed, filterable, orderable query
surface, materialized by the adapter, never by the canonical schema.

- **Config shape.** Per project: `indexedAttributeKeys: [{ key, class }]` where
  `class ∈ {string, numeric, timestamp}` (a closed subset — no maps/refs). Max
  **16 keys per project** (a ceiling to bound index cost). Stored as project
  config, not model contract.
- **Query surface.** These keys appear in the DSL under a reserved namespace so
  they never collide with canonical fields:
  `{ "field": "attr:customer_ref", "op": "eq", "value": "X" }`. The compiler
  validates `attr:<key>` against the **per-project** indexed-key set (resolved at
  compile time from project config), not the static `fields.json`. An `attr:` key
  not configured for the project is `unknown_field` (422), exactly like a bad
  canonical field. `orderBy` on a configured `attr:` key is allowed; the NULL
  policy applies.
- **Index strategy (informative, per adapter).**
  - *Postgres (lite):* a b-tree **expression index** per configured key,
    e.g. `CREATE INDEX ... ON spans ((attributes ->> 'customer_ref'))`, cast for
    numeric/timestamp classes. Created/dropped when project config changes.
  - *ClickHouse (scale):* a **materialized column** (`MATERIALIZED
    attributes['customer_ref']`) with the ordinary skip-index, or a projection.
  - The compiler emits the same DSL-level predicate; each adapter maps
    `attr:<key>` to its physical expression.
- **Explicit non-goal.** This **never** modifies the canonical model: no new
  promoted fields, no schema change, no effect on the merge fold or conformance
  vectors. A canonical field and an indexed attribute key are different things;
  the `attr:` namespace keeps them distinct on the wire.

## Consequences

- Tenants get first-class custom dimensions (Story 2) and ML teams can promote a
  couple of metrics (Story 18) without a contract change — flipping several 🟠s
  toward 🟢 in the audit.
- The canonical promoted set stays small and portable; the pressure to grow it
  is relieved by a per-deployment mechanism rather than by bending invariant 6/D11.
- Adapters carry the materialization cost; the ceiling (16 keys) bounds it. A
  per-project index-management path (create/drop on config change) is the main new
  operational surface.
- The DSL gains one validation mode (per-project field resolution) — additive,
  scoped to the `attr:` namespace, and does not touch canonical-field validation.

## Alternatives considered

- **Promote into the canonical model per request** — rejected: pollutes the
  shared contract with tenant-specific fields; one-way door.
- **Only attributes-map filtering (status quo)** — rejected: not orderable, no
  dedicated index, degrades on high-cardinality keys.
