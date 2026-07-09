# Settings-form schema conventions

Conventions for plugin settings schemas. A plugin declares its configuration as
a JSON Schema; the web shell renders it with `packages/schema-form`
(JSON Schema → settings UI). This directory holds the shared conventions and any
reusable schema fragments (widget hints, secret-field marking so values route
through the `secrets` primitive, validation patterns) that plugin settings build
on.

The goal: a plugin author writes a schema, and gets a consistent, accessible
settings form for free — no bespoke UI.
