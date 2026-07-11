// Self-tests for the SDK data hooks + the @llmobs/plugin-sdk/testing utilities.
// These import the BUILT package (dist) exactly as a plugin author would, proving the
// published surface + the test harness actually work together. Run after `build`.
import * as React from "react";
import { describe, it, expect } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
// Import the BUILT package via its dist entry points (what a plugin author's
// "@llmobs/plugin-sdk" / "@llmobs/plugin-sdk/testing" imports resolve to). Relative
// paths keep vitest's resolver unambiguous; run after `build`.
import { useTraces, useSettings, useWriteScore } from "../dist/react.js";
import { createFakeClient, TestLLMObsProvider, SdkError } from "../dist/testing.js";

function TracesList(): React.ReactElement {
  const { data, loading, error } = useTraces({ from: "2026-01-01T00:00:00Z", to: "2026-01-02T00:00:00Z" });
  if (loading) return <p>loading</p>;
  if (error) return <p>error: {error.message}</p>;
  return <ul>{data?.data.map((t: any) => <li key={t.trace_id}>{t.trace_id}</li>)}</ul>;
}

describe("useTraces against a fake client", () => {
  it("renders the fake rows and records the query", async () => {
    const client = createFakeClient({ traces: [{ trace_id: "t1" }, { trace_id: "t2" }] });
    render(
      <TestLLMObsProvider client={client}>
        <TracesList />
      </TestLLMObsProvider>,
    );
    await waitFor(() => expect(screen.getByText("t1")).toBeDefined());
    expect(screen.getByText("t2")).toBeDefined();
    expect(client.calls.query).toHaveLength(1);
    expect(client.calls.query[0].target).toBe("traces");
  });

  it("surfaces an SdkError as the error state", async () => {
    const client = createFakeClient({ failWith: new SdkError(503, "unavailable", "backend down") });
    render(
      <TestLLMObsProvider client={client}>
        <TracesList />
      </TestLLMObsProvider>,
    );
    await waitFor(() => expect(screen.getByText(/error: backend down/)).toBeDefined());
  });
});

function SettingsForm(): React.ReactElement {
  const { data, loading, save, saving } = useSettings();
  if (loading) return <p>loading</p>;
  return (
    <div>
      <span>endpoint:{String(data?.values.endpoint ?? "")}</span>
      <span>apiKeySet:{String(data?.secrets.apiKey ?? false)}</span>
      <button onClick={() => save({ endpoint: "https://new" })} disabled={saving}>
        save
      </button>
    </div>
  );
}

describe("useSettings against a fake client", () => {
  it("loads the view (secret shown only as set) and saves through the client", async () => {
    const client = createFakeClient({
      settings: { values: { endpoint: "https://old" }, secrets: { apiKey: true } },
    });
    render(
      <TestLLMObsProvider client={client}>
        <SettingsForm />
      </TestLLMObsProvider>,
    );
    await waitFor(() => expect(screen.getByText("endpoint:https://old")).toBeDefined());
    // The secret is reported as set — never a value.
    expect(screen.getByText("apiKeySet:true")).toBeDefined();

    fireEvent.click(screen.getByText("save"));
    await waitFor(() => expect(client.calls.setSettings).toHaveLength(1));
    expect(client.calls.setSettings[0]).toEqual({ endpoint: "https://new" });
    // A save with no secret field omits it (kernel preserves the stored secret).
    expect("apiKey" in client.calls.setSettings[0]).toBe(false);
  });
});

function ScoreButton(): React.ReactElement {
  const { mutate, loading } = useWriteScore();
  return (
    <button
      disabled={loading}
      onClick={() =>
        void mutate({
          id: "s1",
          subject_type: "trace",
          subject_id: "t1",
          name: "quality",
          data_type: "numeric",
          value_numeric: 1,
          source: "test",
          timestamp: "2026-01-01T00:00:00Z",
          environment: "dev",
        })
      }
    >
      score
    </button>
  );
}

describe("useWriteScore against a fake client", () => {
  it("records the written score", async () => {
    const client = createFakeClient({ writeScoreResult: { id: "s1" } });
    render(
      <TestLLMObsProvider client={client}>
        <ScoreButton />
      </TestLLMObsProvider>,
    );
    fireEvent.click(screen.getByText("score"));
    await waitFor(() => expect(client.calls.writeScore).toHaveLength(1));
    expect(client.calls.writeScore[0].name).toBe("quality");
  });
});

// N2: a CUSTOM settings view — NOT SchemaForm — renders opaque (nested) settings via
// useSettings and persists them. This proves the frontend half of the custom-settings
// escape: a rule-builder/visual-editor renders + saves through the same generic hook,
// while the kernel stores the opaque values and keeps declared secrets never-returned.
function CustomSettings(): React.ReactElement {
  const { data, loading, save, saving } = useSettings();
  if (loading) return <p>loading</p>;
  const rules = (data?.values?.rules as Array<{ op: string }> | undefined) ?? [];
  return (
    <div>
      <p>rules: {rules.length}</p>
      <p>op0: {rules[0]?.op ?? "none"}</p>
      <p>apiKey set: {data?.secrets?.apiKey ? "yes" : "no"}</p>
      <button
        onClick={() => save({ rules: [{ op: "eq" }, { op: "gt" }], layout: { columns: 3 } })}
        disabled={saving}
      >
        Save
      </button>
    </div>
  );
}

describe("custom (non-SchemaForm) settings view via useSettings (N2)", () => {
  it("renders opaque nested values + secret markers, and persists a custom shape", async () => {
    const saved: Array<Record<string, unknown>> = [];
    const client = createFakeClient({
      settings: { values: { rules: [{ op: "eq" }] }, secrets: { apiKey: true } },
      onSetSettings: (v) => {
        saved.push(v);
      },
    });
    render(
      <TestLLMObsProvider client={client}>
        <CustomSettings />
      </TestLLMObsProvider>,
    );
    // The nested opaque value + secret marker render (no SchemaForm involved).
    await waitFor(() => expect(screen.getByText("rules: 1")).toBeTruthy());
    expect(screen.getByText("op0: eq")).toBeTruthy();
    expect(screen.getByText("apiKey set: yes")).toBeTruthy();

    // Saving a nested (non-flat-subset) shape round-trips through the generic hook.
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => expect(saved.length).toBe(1));
    expect((saved[0].rules as unknown[]).length).toBe(2);
    expect((saved[0].layout as { columns: number }).columns).toBe(3);
  });
});
