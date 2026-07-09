# Canonical data model

The canonical data model spec — the shape everything normalizes to and the Query
API speaks in. OTLP + GenAI semantic conventions are the native format (D3);
dialect normalizers map other wire formats onto this model, always preserving
raw attributes alongside the canonical fields.

This directory holds:

- The **model spec** (markdown): the canonical span / trace / score entities,
  their fields, and their semantics.
- The **schema** for those entities, from which `kernel/pkg/model` (Go) and the
  TS types are generated via `tools/codegen`.

This is the next design session's primary output (see the bootstrap order). Treat
it as the most load-bearing contract in the repo: it changes first, and code —
storage adapters, normalizers, the DSL planner, the SDK — follows.
