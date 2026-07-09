# Template: plugin-python

Scaffold for a Python plugin: a FastAPI backend, a Module Federation frontend, a
`llmobs-plugin.yaml` manifest, and a CI workflow. This is the reference target
for the "your first plugin in Python" tutorial (`docs/plugin-authors/`).

The backend speaks the plugin protocol over HTTP and uses only the public plugin
API; the frontend imports only `@llmobs/plugin-sdk` (+ `@llmobs/ui`,
`@llmobs/tokens`). No infrastructure access.
