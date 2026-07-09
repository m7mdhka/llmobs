---
scope: ["kernel/**", "cli/**"]
---

# Go style

- **Standard library first.** Reach for a dependency only when the stdlib
  genuinely can't do it; adding a hot-path dependency needs an ADR.
- **Errors wrapped with context.** `fmt.Errorf("doing X: %w", err)`. Never
  discard an error silently. No panics outside `main`.
- **Context propagation.** Pass `context.Context` as the first argument on any
  function that does I/O or crosses a boundary; honor cancellation.
- **Small, explicit interfaces, defined at the consumer.** Don't ship a giant
  interface from the producer; the caller declares the narrow interface it needs.
- **No global state.** No package-level mutable singletons; pass dependencies
  explicitly (constructors return structs with their collaborators).
- **Table-driven tests.** Prefer `tests := []struct{...}` with subtests
  (`t.Run`). Keep tests hermetic; no network, no wall-clock flakiness.
- **`internal/` is private by construction.** Nothing under `kernel/internal/`
  is importable from `cli/`, `web/`, or plugins — do not try to widen it.
- Branding comes from `kernel/pkg/brand` — never hardcode the product name.
