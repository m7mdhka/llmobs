import React from "react";
import { Link } from "react-router-dom";
import { ErrorState, LoadingState, SchemaForm, useSettings } from "@llmobs/plugin-sdk/react";
import schema from "./settings.schema.json";

// The Tracing plugin's settings tab — the dogfood of J2 (ADR-0024). A PURE-FRONTEND
// plugin persists its own config with NO backend: useSettings() reads/writes through
// the frontend-token-scoped settings store, and SchemaForm renders the very schema
// (settings.schema.json) the kernel validates against — one source of truth. The
// `exportApiKey` field is writeOnly, so it is stored encrypted and never rendered
// back: the form only knows whether it is set.
export function SettingsPage(): React.ReactElement {
  const { data, loading, error, save, saving, saveError } = useSettings();

  if (loading) return <LoadingState title="Loading settings…" />;
  if (error) return <ErrorState title="Couldn't load settings" body={error.message} />;

  return (
    <section className="tracing-settings">
      <header className="tracing-settings__head">
        <h1>Tracing settings</h1>
        <Link to="..">← Back to traces</Link>
      </header>
      <SchemaForm
        schema={schema}
        values={data?.values}
        secretsSet={data?.secrets}
        onSubmit={save}
        submitting={saving}
        serverError={saveError?.message}
      />
    </section>
  );
}
