# Template: plugin-frontend-only

Scaffold for a **Tier-2** plugin with no backend — a Module Federation frontend
surface plus a `llmobs-plugin.yaml` manifest. Useful for tabs that only compose
existing data via the SDK's `query` primitive and render it.

The frontend imports only `@llmobs/plugin-sdk` (+ `@llmobs/ui`,
`@llmobs/tokens`).
