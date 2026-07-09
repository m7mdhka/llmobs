---
scope: ["plugins/**"]
---

# Plugin boundary (the dogfood rule, D2)

First-party plugins are structured exactly like third-party plugins and get no
special access. This is enforced by CI, not discipline.

- **Allowed imports only:**
  - Go: `github.com/m7mdhka/llmobs/kernel/pkg/pluginproto`,
    `github.com/m7mdhka/llmobs/kernel/pkg/model`. Nothing else from the kernel.
  - TS: `@llmobs/plugin-sdk` (and `@llmobs/ui`, `@llmobs/tokens` for frontend).
- **Never import `kernel/internal/...`.** If you need something that isn't
  exposed, the plugin API has a gap — propose the API change in `api/` + SDK
  instead. Do not fight the import-boundary lint.
- **No infrastructure access.** No DB drivers, no Redis/NATS clients, no S3
  SDK. Everything goes through the seven SDK primitives (query, write, events,
  jobs, kv, secrets, surface).
- **Manifest must validate** against `api/schemas/manifest/v1alpha1`. The
  `llmobs-plugin.yaml` is the contract for how the kernel runs the plugin.
- **Structure mirrors the templates.** Each plugin has `backend/` (Go),
  `frontend/` (TS), `manifest`, `migrations/`, and tests — the same shape a
  third party would scaffold from `templates/`.
- **Identity is never self-asserted.** Trust only kernel-signed assertions;
  carry the service token + user assertion on Query API calls.
