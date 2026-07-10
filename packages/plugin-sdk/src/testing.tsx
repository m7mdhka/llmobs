// @llmobs/plugin-sdk/testing — utilities for unit-testing a plugin surface's hooks
// (useQuery/useTraces/useTrace/useWriteScore/useSettings) WITHOUT a running kernel.
// Import from "@llmobs/plugin-sdk/testing" in your tests (framework-agnostic; pair
// with your own test runner + a DOM, e.g. vitest + jsdom + @testing-library/react).
//
//   const client = createFakeClient({ traces: [{ trace_id: "t1" }] });
//   render(<TestLLMObsProvider client={client}><MyTab /></TestLLMObsProvider>);
//   // assert on what rendered, then inspect client.calls.query, etc.
import * as React from "react";
import { LLMObsPluginProvider } from "./context.js";
import { SdkError } from "./client.js";
import type {
  LLMObsClient,
  QueryInput,
  QueryResponse,
  ScoreInput,
  SettingsView,
  TraceTree,
} from "./client.js";

// RecordedCalls captures every call a hook made, so a test can assert the surface
// issued the right query / wrote the right score / saved the right settings.
export interface RecordedCalls {
  query: QueryInput[];
  traceTree: string[];
  writeScore: ScoreInput[];
  getSettings: number;
  setSettings: Array<Record<string, unknown>>;
}

export interface FakeClientOptions {
  /** Rows returned by query(), chosen by the document's `target`. */
  traces?: unknown[];
  spans?: unknown[];
  scores?: unknown[];
  /** Full control over query() — overrides the target-based rows above. */
  onQuery?: (doc: QueryInput) => QueryResponse;
  /** The tree returned by traceTree(). */
  traceTree?: TraceTree;
  /** The id returned by writeScore() (default "score-test-1"). */
  writeScoreResult?: { id: string };
  /** The view returned by getSettings() (default empty). */
  settings?: SettingsView;
  /** Called on setSettings() — throw an SdkError to simulate a kernel rejection. */
  onSetSettings?: (values: Record<string, unknown>) => void | Promise<void>;
  /** When set, EVERY call rejects with this error (simulate an unavailable backend). */
  failWith?: SdkError;
}

export interface FakeClient extends LLMObsClient {
  /** Everything the hooks called, in order. */
  readonly calls: RecordedCalls;
  /** Replace the settings view a subsequent getSettings() returns (e.g. after a save). */
  setSettingsView(view: SettingsView): void;
}

function emptyResponse<T>(rows: T[]): QueryResponse<T> {
  return { version: "v1alpha1", data: rows, stats: { elapsed_ms: 0, returned: rows.length }, warnings: [] };
}

/**
 * createFakeClient builds an in-memory LLMObsClient for tests: it returns canned data,
 * records every call, and can simulate errors. No network, no kernel.
 */
export function createFakeClient(opts: FakeClientOptions = {}): FakeClient {
  const calls: RecordedCalls = { query: [], traceTree: [], writeScore: [], getSettings: 0, setSettings: [] };
  let settingsView: SettingsView = opts.settings ?? { values: {}, secrets: {} };
  const reject = <T,>(): Promise<T> => Promise.reject(opts.failWith as SdkError);

  return {
    calls,
    setSettingsView(view) {
      settingsView = view;
    },
    query<T = unknown>(doc: QueryInput): Promise<QueryResponse<T>> {
      calls.query.push(doc);
      if (opts.failWith) return reject<QueryResponse<T>>();
      if (opts.onQuery) return Promise.resolve(opts.onQuery(doc) as QueryResponse<T>);
      const rows =
        doc.target === "traces" ? opts.traces : doc.target === "scores" ? opts.scores : opts.spans;
      return Promise.resolve(emptyResponse((rows ?? []) as T[]));
    },
    traceTree(traceId: string): Promise<TraceTree> {
      calls.traceTree.push(traceId);
      if (opts.failWith) return reject<TraceTree>();
      return Promise.resolve(
        opts.traceTree ?? ({ trace: { trace_id: traceId } as unknown as TraceTree["trace"], spans: [] }),
      );
    },
    writeScore(score: ScoreInput): Promise<{ id: string }> {
      calls.writeScore.push(score);
      if (opts.failWith) return reject<{ id: string }>();
      return Promise.resolve(opts.writeScoreResult ?? { id: "score-test-1" });
    },
    getSettings(): Promise<SettingsView> {
      calls.getSettings += 1;
      if (opts.failWith) return reject<SettingsView>();
      return Promise.resolve(settingsView);
    },
    async setSettings(values: Record<string, unknown>): Promise<void> {
      calls.setSettings.push(values);
      if (opts.failWith) throw opts.failWith;
      if (opts.onSetSettings) await opts.onSetSettings(values);
    },
  };
}

/**
 * TestLLMObsProvider wraps a plugin surface in the SDK provider backed by a fake
 * client, so the hooks resolve against `client` instead of the network. Everything
 * else (project, user) has sensible test defaults you can override.
 */
export function TestLLMObsProvider({
  client,
  projectId = "test-project",
  user = { id: "u-test", email: "test@example.com", role: "admin" },
  children,
}: {
  client: LLMObsClient;
  projectId?: string;
  user?: { id: string; email: string; role: string };
  children: React.ReactNode;
}): React.ReactElement {
  return (
    <LLMObsPluginProvider config={{ client, projectId, user }}>{children}</LLMObsPluginProvider>
  );
}

// Re-export SdkError so tests can construct expected errors (e.g. new SdkError(400, ...)),
// and LLMObsClient so a test can type a hand-rolled client that isn't the fake.
export { SdkError };
export type { LLMObsClient };
