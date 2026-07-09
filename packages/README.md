# packages/

TypeScript workspace packages (pnpm + Turborepo). Shared frontend/SDK code.

| Package | Purpose |
|---|---|
| `plugin-sdk/` | ★ `@llmobs/plugin-sdk` — the seven primitives (query, write, events, jobs, kv, secrets, surface), hooks, MF runtime glue, and unavailable-state components. **Semver-sacred**: a breaking change is a major bump (D8). |
| `ui/` | Design system: clone-and-own shadcn/Radix components, precompiled. Never add shadcn via CLI — copy into the package and own it. |
| `tokens/` | CSS custom properties, light/dark themes. The only source of colors/spacing/typography. |
| `query-client/` | **Generated** from `api/openapi` — never hand-written. `make generate` regenerates it. |
| `schema-form/` | JSON Schema → settings UI renderer. |
| `brand/` | D15: the one place the product name lives (TS). Twin of `kernel/pkg/brand`. |

See [`.claude/rules/frontend.md`](../.claude/rules/frontend.md).
