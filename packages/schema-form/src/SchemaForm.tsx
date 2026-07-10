import * as React from "react";
import { Button, Field } from "@llmobs/ui";
import { parseSchema, type Field as SchemaField, type Model } from "./schema.js";
import { validate, hasErrors, type Errors } from "./validate.js";

export interface SchemaFormProps {
  /** Raw settings JSON Schema (the supported subset). */
  schema: unknown;
  /** Current non-secret values (from the settings `get`). */
  values?: Record<string, unknown>;
  /** Per-secret field: whether a value is already stored. Secrets are NEVER
   *  prefilled — only their "set" state is known. */
  secretsSet?: Record<string, boolean>;
  /** Persist the changed values. Secret fields left blank are OMITTED so the kernel
   *  preserves the stored secret. */
  onSubmit: (values: Record<string, unknown>) => Promise<void> | void;
  /** True while a save is in flight (disables the button). */
  submitting?: boolean;
  /** A server-side error to surface (e.g. a validation error the kernel returned). */
  serverError?: string;
}

/**
 * SchemaForm renders a plugin's settings schema as an accessible form using
 * @llmobs/ui, validates on submit against the same subset the kernel enforces, and
 * treats `writeOnly` fields as write-only secrets: they are never prefilled, show
 * only a "set/not set" hint, and are submitted only when the user types a new value.
 */
export function SchemaForm({
  schema,
  values,
  secretsSet,
  onSubmit,
  submitting,
  serverError,
}: SchemaFormProps): React.ReactElement {
  const model = React.useMemo<Model | { error: string }>(() => {
    try {
      return parseSchema(schema);
    } catch (e) {
      return { error: e instanceof Error ? e.message : String(e) };
    }
  }, [schema]);

  if ("error" in model) {
    return <p className="llm-form__error" role="alert">Unsupported settings schema: {model.error}</p>;
  }

  return (
    <FormBody
      model={model}
      values={values ?? {}}
      secretsSet={secretsSet ?? {}}
      onSubmit={onSubmit}
      submitting={submitting}
      serverError={serverError}
    />
  );
}

function initialState(model: Model, values: Record<string, unknown>): Record<string, unknown> {
  const s: Record<string, unknown> = {};
  for (const f of model.fields) {
    if (f.secret) {
      s[f.name] = ""; // secrets never prefilled
    } else if (f.name in values) {
      s[f.name] = values[f.name];
    } else if (f.default !== undefined) {
      s[f.name] = f.default;
    } else {
      s[f.name] = f.type === "boolean" ? false : "";
    }
  }
  return s;
}

function FormBody({
  model,
  values,
  secretsSet,
  onSubmit,
  submitting,
  serverError,
}: {
  model: Model;
  values: Record<string, unknown>;
  secretsSet: Record<string, boolean>;
  onSubmit: (v: Record<string, unknown>) => Promise<void> | void;
  submitting?: boolean;
  serverError?: string;
}): React.ReactElement {
  const [state, setState] = React.useState<Record<string, unknown>>(() => initialState(model, values));
  const [errors, setErrors] = React.useState<Errors>({});

  const set = (name: string, v: unknown) => setState((s) => ({ ...s, [name]: v }));

  const build = (): Record<string, unknown> => {
    // Coerce and drop untouched secrets so the kernel preserves them.
    const out: Record<string, unknown> = {};
    for (const f of model.fields) {
      const v = state[f.name];
      if (f.secret) {
        if (typeof v === "string" && v !== "") out[f.name] = v;
        continue;
      }
      if (f.type === "number" || f.type === "integer") {
        out[f.name] = v === "" || v === undefined ? undefined : Number(v);
      } else {
        out[f.name] = v;
      }
    }
    // Remove undefined (unset optional numbers) so they are not sent as null.
    for (const k of Object.keys(out)) if (out[k] === undefined) delete out[k];
    return out;
  };

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const payload = build();
    const errs = validate(model, payload, secretsSet);
    setErrors(errs);
    if (!hasErrors(errs)) void onSubmit(payload);
  };

  return (
    <form className="llm-form" onSubmit={submit} noValidate>
      {model.fields.map((f) => (
        <FieldRow
          key={f.name}
          field={f}
          value={state[f.name]}
          secretSet={!!secretsSet[f.name]}
          error={errors[f.name]}
          onChange={(v) => set(f.name, v)}
        />
      ))}
      {serverError ? (
        <p className="llm-form__error" role="alert">{serverError}</p>
      ) : null}
      <Button type="submit" disabled={submitting}>
        {submitting ? "Saving…" : "Save settings"}
      </Button>
    </form>
  );
}

function FieldRow({
  field: f,
  value,
  secretSet,
  error,
  onChange,
}: {
  field: SchemaField;
  value: unknown;
  secretSet: boolean;
  error?: string;
  onChange: (v: unknown) => void;
}): React.ReactElement {
  const id = `set-${f.name}`;
  const label = f.title ?? f.name;
  const describedBy = f.description ? `${id}-desc` : undefined;

  const control = (() => {
    if (f.secret) {
      return (
        <input
          id={id}
          className="llm-input"
          type="password"
          value={typeof value === "string" ? value : ""}
          placeholder={secretSet ? "•••••••• (set — leave blank to keep)" : "Enter a value"}
          aria-describedby={describedBy}
          autoComplete="new-password"
          onChange={(e) => onChange(e.target.value)}
        />
      );
    }
    if (f.type === "boolean") {
      return (
        <input
          id={id}
          type="checkbox"
          checked={value === true}
          aria-describedby={describedBy}
          onChange={(e) => onChange(e.target.checked)}
        />
      );
    }
    if (f.enum && f.enum.length > 0) {
      return (
        <select
          id={id}
          className="llm-input"
          value={typeof value === "string" ? value : ""}
          aria-describedby={describedBy}
          onChange={(e) => onChange(e.target.value)}
        >
          <option value="" disabled>
            Select…
          </option>
          {f.enum.map((opt) => (
            <option key={opt} value={opt}>
              {opt}
            </option>
          ))}
        </select>
      );
    }
    const numeric = f.type === "number" || f.type === "integer";
    return (
      <input
        id={id}
        className="llm-input"
        type={numeric ? "number" : "text"}
        step={f.type === "integer" ? 1 : "any"}
        value={value === undefined || value === null ? "" : String(value)}
        aria-describedby={describedBy}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  })();

  return (
    <Field label={f.required ? `${label} *` : label} htmlFor={id}>
      {control}
      {f.description ? (
        <p id={describedBy} className="llm-field__hint">
          {f.description}
        </p>
      ) : null}
      {error ? (
        <p className="llm-field__error" role="alert">
          {error}
        </p>
      ) : null}
    </Field>
  );
}
