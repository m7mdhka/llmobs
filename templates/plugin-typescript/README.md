# Template: plugin-typescript

Scaffold for a TypeScript plugin: a TS backend, a Module Federation frontend, a
`llmobs-plugin.yaml` manifest, and a CI workflow.

The backend uses only the public plugin API; the frontend imports only
`@llmobs/plugin-sdk` (+ `@llmobs/ui`, `@llmobs/tokens`). No infrastructure
access.
