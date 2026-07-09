---
scope: ["web/**", "packages/**"]
---

# Frontend

- **Tokens only — no hardcoded hex.** Colors, spacing, and typography come from
  `@llmobs/tokens` (CSS custom properties, light/dark). Never inline a hex value.
- **Design system is clone-and-own.** `packages/ui` holds shadcn/Radix
  components copied into the package. Never add shadcn via its CLI; copy the
  component in and own it.
- **Module Federation shared-singleton rules.** React, the SDK, and the design
  system are shared singletons across the shell and plugins. Don't bundle a
  second copy; respect the shared-deps configuration.
- **No `localStorage` in SDK components.** State that must persist goes through
  the SDK's `kv` primitive, not browser storage — SDK components must work
  identically wherever the shell mounts them.
- **Accessibility basics are non-negotiable.** Semantic elements, labelled
  controls, keyboard operability, visible focus, sufficient contrast (the tokens
  are designed to pass — don't override them into failing).
- **Generated client is off-limits to hand edits.** `packages/query-client` is
  generated from `api/openapi`; change the spec and run `make generate`.
- **Render unavailable/degraded plugin states.** Use the SDK's
  unavailable-state components; a plugin surface must degrade gracefully.
