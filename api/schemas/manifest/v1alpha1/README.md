# Plugin manifest schema — v1alpha1

JSON Schema for `llmobs-plugin.yaml`, the manifest every plugin ships (D4). The
manifest is the contract for how the kernel runs a plugin: its namespaced id
(`owner/plugin-name`), requested capabilities (only the seven data primitives),
surfaces (frontend module-federation entries, SHA-pinned at install), settings
schema reference, and supervisor/runtime hints.

The canonical schema file is `llmobs-plugin.schema.json` (referenced by
`.vscode/settings.json` for editor autocomplete — we dogfood our own schema from
day one).

Additive-only at this maturity level; breaking changes require an ADR and a new
maturity directory.
