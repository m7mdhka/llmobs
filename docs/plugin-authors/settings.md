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
tenant. Settings are **project-shared**, so **writes require configuration authority**
— a read-only viewer can `get` (to render the tab) but gets `403` on `set`, so they
cannot overwrite shared config or a stored secret. (Admins today; the finer RBAC is
the #21 seam.) Your form can still render for everyone — a viewer's save surfaces the
`403` via `saveError`; hide the Save button for read-only users if you prefer.

Like all frontend-token access, this is **not a boundary against a hostile same-origin
frontend** (see [trust-model.md](trust-model.md)). Secrets are safe regardless: they
are encrypted at rest and never returned to any client. Settings that must be
tamper-proof against the plugin's own code belong in a backend.

## Endpoints (reference)

- `POST /v1alpha1/plugin/settings/get` → `{ "values": {…}, "secrets": { "<field>": true|false } }`
- `POST /v1alpha1/plugin/settings/set` with `{ "values": { "<field>": <value>, … } }` → `204`, or `400 { error, field }` on a validation failure.

Both are frontend-token authed; the SDK presents the token for you.

## Custom settings view (the escape hatch, N2)

The SchemaForm path renders standard field types. If your settings need a **custom UI** —
a rule-builder, a visual mapping editor, anything the flat subset can't express — declare
`spec.settingsView: custom` in your manifest and render your own settings view (any
framework, via the [neutral mount contract](../adr/0030-framework-neutral-frontend-contract.md)).
You still read and write through the same `useSettings` hook:

```ts
const { data, save } = useSettings();
// data.values is your OPAQUE settings object (any shape you stored)
save({ rules: [/* your nested rule tree */], layout: { columns: 3 } });
```

- **Non-secret values are opaque.** The kernel stores whatever JSON you send (bounded to
  64 KiB) without subset-validation — your view owns validation. Nested objects and arrays
  are fine here (unlike the schema-form subset).
- **Secrets are unchanged.** Declare a `writeOnly` field in a (now-optional) `settingsSchema`
  and it is still envelope-encrypted and **never returned** — H4 holds identically in
  custom mode. A custom plugin with no secrets needs no schema at all.
- **Schema mode stays the default.** Use it (no `settingsView`, or `settingsView: schema`)
  whenever the flat subset fits — you get a consistent, validated form for free.
