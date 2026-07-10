# Plugin settings — a schema-form tab with no backend

A plugin gets a settings tab by declaring **one JSON Schema** and rendering **one
component**. The kernel stores the values scoped to your plugin and tenant, validates
every write, and keeps `writeOnly` fields secret. A *pure-frontend* plugin needs no
backend for this — settings persist through the J1 frontend token (ADR-0024).

## 1. Declare the schema in your manifest

```yaml
spec:
  # path is relative to the plugin dir (where llmobs-plugin.yaml lives)
  settingsSchema: frontend/src/settings.schema.json
```

## 2. Write the schema (the supported subset)

Settings are a **flat** object. Each property is one of four types, plus a few
constraints. The **same** subset is validated client-side and by the kernel, so what
the form accepts is exactly what the kernel accepts.

```json
{
  "type": "object",
  "required": ["pageSize"],
  "properties": {
    "defaultTimeRange": { "type": "string", "title": "Default range", "enum": ["1h", "24h", "7d"], "default": "24h" },
    "pageSize":         { "type": "integer", "title": "Rows per page", "minimum": 10, "maximum": 200, "default": 50 },
    "showSpanEvents":   { "type": "boolean", "title": "Show span events" },
    "exportApiKey":     { "type": "string", "title": "Export API key", "writeOnly": true }
  }
}
```

Supported: types `string | number | integer | boolean`; `enum` (string), `required`,
`minLength`, `minimum`/`maximum`, `title`, `description`, `default`. Anything else
(nested objects, arrays, `oneOf`, …) fails conformance — keep settings flat.

## 3. Secrets — `writeOnly: true`

A `writeOnly` string is a **secret**:

- stored **envelope-encrypted** (the same box as the `secrets` primitive);
- **never returned** — a `get` tells you only whether it is *set*, never the value;
- **preserved** when a save omits it or sends `""` — re-saving the form does not wipe
  a secret the user did not retype.

So a settings form can show "•••• (set)" and let the user replace the value, without
the value ever leaving the kernel.

## 4. Render it

`useSettings()` loads the current values (and which secrets are set) and gives you a
`save`; `<SchemaForm>` renders the schema and validates on submit. Both come from
`@llmobs/plugin-sdk`.

```tsx
import { SchemaForm, useSettings, LoadingState, ErrorState } from "@llmobs/plugin-sdk";
import schema from "./settings.schema.json";

export function SettingsTab() {
  const { data, loading, error, save, saving, saveError } = useSettings();
  if (loading) return <LoadingState title="Loading settings…" />;
  if (error) return <ErrorState title="Couldn't load settings" body={error.message} />;
  return (
    <SchemaForm
      schema={schema}
      values={data?.values}          // non-secret values
      secretsSet={data?.secrets}     // { field: isSet } — secrets are never prefilled
      onSubmit={save}
      submitting={saving}
      serverError={saveError?.message}
    />
  );
}
```

That is the whole feature. See `plugins/tracing` for a working first-party example (a
pure-frontend plugin persisting `defaultTimeRange`, `pageSize`, `showSpanEvents`, and
a `writeOnly` `exportApiKey`).

## How it is scoped (and what that does NOT protect)

Settings calls are authed by the **frontend token**: the plugin id and project come
from the token, so a plugin reads/writes only **its own** settings in the **current**
tenant. This is least-privilege by default — but, like all frontend-token access, it
is **not a boundary against a hostile same-origin frontend** (see
[trust-model.md](trust-model.md)). Secrets are safe regardless: they are encrypted at
rest and never returned to any client. Settings that must be tamper-proof against the
plugin's own code belong in a backend.

## Endpoints (reference)

- `POST /v1alpha1/plugin/settings/get` → `{ "values": {…}, "secrets": { "<field>": true|false } }`
- `POST /v1alpha1/plugin/settings/set` with `{ "values": { "<field>": <value>, … } }` → `204`, or `400 { error, field }` on a validation failure.

Both are frontend-token authed; the SDK presents the token for you.
