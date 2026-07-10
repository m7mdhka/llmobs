# ADR-0021: Explicit-clear sentinel (one merge algorithm survives)

- **Status:** Accepted (spec change rides with the next contracts PR; V18 below)
- **Date:** 2026-07-10
- **Deciders:** m7mdhka (flexibility-audit remediation)
- **Relates to:** ADR-0016 (canonical model), `05-update-semantics.md` (merge).
  Audit Story 20 (Hana).

## Context

The merge fold is empty-never-clobbers (`05-update-semantics.md`): an absent or
empty field in a later event never overwrites a present value. This is what makes
delta updates safe and is **normative and deliberately non-configurable** —
per-project merge modes would break cross-adapter determinism (the conformance
V-vectors assume one algorithm), plugin-visible event semantics (a plugin cannot
reason about state if the fold varies by project), and the per-field provenance
model.

Hana (Story 20) has a snapshot SDK that always sends the *complete* span state,
and clears a field by sending it back to `null`. Under empty-never-clobbers, that
intentional clear is silently ignored. She asked for "last-snapshot-wins, clobber
allowed" — a second merge algorithm. We refuse the second algorithm but must
serve the legitimate need: an **explicit, intentional clear**.

## Decision

Add a single reserved **clear sentinel** to the merge contract. Exactly one merge
algorithm survives; empty still never clobbers, but an *explicit clear* is not an
empty value — it is a distinct, intentional instruction.

- **Wire representation.** The reserved string token **`"@@@llmobsClear@@@"`** as
  a field's value in an event payload means "unset this field-group." It is
  chosen to match the reserved-token style of media references (`02-span.md §7.1`)
  and cannot occur as a legitimate value. (A structural alternative — a
  top-level `"clear": ["field", ...]` array on the event — was considered; the
  in-band sentinel is simpler for snapshot SDKs that already emit full objects.)
- **Interaction with empty-never-clobbers.** An ordinary empty/absent value is
  still ignored (no clobber). The sentinel is the *only* way a later event
  removes a previously-set value. After a clear, the field-group is absent from
  the observable entity (as if never set), until a later event sets it again.
- **Frozen fields.** Clearing a frozen field (`id`, `project_id`, `trace_id`,
  `kind`, `start_time`, `environment`, and the score frozen set) is **always a
  violation** — the sentinel on a frozen field is rejected as a data-quality
  error (`llmobs.dq.frozen_field_clear`), never applied. Frozen means frozen.
- **Provenance.** A clear is stamped like any field-group write: the clearing
  event's `(event_ts, event_id)` becomes the group's provenance, and
  out-of-order rules apply — a clear at t=5 loses to a set at t=6 and wins over a
  set at t=4, exactly as a normal write would. Order-independence (the shuffle
  property) must hold for clears too.
- **Tombstone unaffected.** `is_deleted` (whole-entity delete) is orthogonal; the
  clear sentinel operates on a single field-group, not the entity.

## V18 (for the next spec revision of `05-update-semantics.md` §6)

```json
{
  "name": "V18",
  "entity": "span",
  "note": "explicit-clear sentinel removes a previously-set scalar; empty would not",
  "events": [
    { "op": "upsert", "event_ts": 1, "event_id": "a",
      "payload": { "id": "s1", "project_id": "p", "name": "first", "release": "1.0" } },
    { "op": "upsert", "event_ts": 2, "event_id": "b",
      "payload": { "release": "@@@llmobsClear@@@" } }
  ],
  "expect": { "id": "s1", "project_id": "p", "name": "first", "is_deleted": false }
}
```

The expectation: `release` is absent after the clear (removed at t=2), while
`name` (untouched) and the frozen fields remain. An out-of-order companion vector
(clear at t=2, a later `release` set at t=3 → `release` present) should accompany
it to pin the provenance interaction.

## Consequences

- Snapshot SDKs get intentional field-clearing without a second merge mode; one
  algorithm, one conformance suite, cross-adapter determinism preserved.
- The merge contract gains one reserved token and one data-quality signal —
  additive. Adapters implement the sentinel inside the existing fold; the shuffle
  conformance test extends to cover clears.
- Producers that never send the sentinel are entirely unaffected (delta semantics
  unchanged).

## Alternatives considered

- **Per-project configurable merge mode** (Hana's request) — rejected: breaks
  determinism, conformance, and plugin-visible semantics (the invariant this ADR
  protects).
- **Treat `null` as clear** — rejected: `null` and absent are indistinguishable
  in many encodings and would make every snapshot SDK's normal payload
  destructive; the whole point of empty-never-clobbers is that emptiness is safe.
