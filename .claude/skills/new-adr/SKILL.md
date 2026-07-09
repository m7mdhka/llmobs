---
name: new-adr
description: Create an Architecture Decision Record in docs/adr with correct numbering, the standard template, and links to related ADRs. Use for any new architectural decision (new hot-path dependency, new public API, storage schema change, new top-level directory).
---

# New ADR

Architectural decisions are recorded as immutable, numbered ADRs. An ADR is
required in the same PR for: a new hot-path dependency, a new public API, a
storage schema change, a new top-level directory, or breaking a published
contract.

## Steps

1. **Find the next number.** List `docs/adr/`, take the highest `NNNN-*.md`, add
   one, zero-pad to 4 digits. (ADR-0001..0015 backfill the D-table.)
2. **Create `docs/adr/NNNN-short-kebab-title.md`** from the template below.
3. **Link related ADRs** at the bottom (supersedes / superseded-by / relates-to).
4. If this ADR supersedes another, set the old one's status to `Superseded by
   ADR-NNNN` (that status line is the only permitted edit to an accepted ADR).

## Template

```markdown
# ADR-NNNN: <Title>

- **Status:** Proposed | Accepted | Superseded by ADR-XXXX
- **Date:** YYYY-MM-DD
- **Deciders:** <maintainers>

## Context
What forces are at play? What problem or constraint prompts a decision?

## Decision
The decision, stated plainly and actively ("We will …").

## Consequences
Positive, negative, and neutral outcomes. What becomes easier; what becomes
harder; what we are now committed to.

## Alternatives considered
Options we rejected and why.

## Links
- Relates to ADR-XXXX
- Implements design decision D<n> (see repository design doc)
```

## Rules
- ADRs are immutable once **Accepted** (except the status line for supersession).
- One decision per ADR. Keep it short and specific.
