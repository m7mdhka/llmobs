# Plugin authors

The home of THE tutorial — "your first plugin in Python" — plus the plugin API
reference. A plugin author should be able to ship a custom tab as a container +
manifest without ever reading kernel code.

Covers:

- The seven SDK primitives (query, write, events, jobs, kv, secrets, surface).
- The **framework-neutral frontend contract** (ADR-0030): your exposed module exports
  `mount(element, context) → unmount` and may render with **any framework** — React,
  Vue, Svelte, or plain DOM. React is one binding (`@llmobs/plugin-sdk/react`, with the
  hooks + design system + `createReactBinding`), not a requirement. Scaffold from
  `templates/plugin-frontend-only` (React) or `templates/plugin-frontend-vanilla`
  (no framework). The `context` carries the token-confined data client, the active
  project, the theme, the routing base path, and locale/direction.
- **Localization + RTL (N3).** The mount `context` carries `locale` (BCP-47) and
  `direction` (`ltr`/`rtl`) — the shell resolves them and stamps `lang`/`dir` on the
  document root. `@llmobs/ui` uses logical CSS properties, so its components flip for RTL
  automatically; a custom view flips by honoring `context.direction`. Externalize your
  strings through the seam: `createTranslator(catalog, context.locale)` returns a
  `t(key, fallback)` (exact-locale → primary-subtag → your source string), so a deployment
  localizes by adding a catalog — no code change. This ships the *mechanism* to localize,
  not translations.
- The `llmobs-plugin.yaml` manifest.
- [The inner loop — `make dev` (create → dev → edit → reload)](dev-loop.md) (J3).
- [Settings — a schema-form tab with no backend](settings.md) (J2).
- The [plugin trust model](trust-model.md) — what the frontend token confines (J1).
- Scaffolding from `templates/` via `llmobs plugin create`.
- The conformance ("verified") bar.
- Distribution: plugin = repo; release = manifest + frontend tarball + OCI image
  digest + sigstore signature + SBOM (D12).

Kept in sync with merged changes by the `docs-writer` agent.
