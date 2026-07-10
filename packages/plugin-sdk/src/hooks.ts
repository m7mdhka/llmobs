import * as React from "react";
import type { LLMObsCanonicalTraceV1Alpha1 as Trace } from "@llmobs/query-client";
import { useLLMObs } from "./context.js";
import {
  SdkError,
  type QueryInput,
  type QueryResponse,
  type ScoreInput,
  type SettingsView,
  type TraceTree,
} from "./client.js";

// Every hook returns the same async shape so surfaces render the standard
// loading/error/empty states uniformly. `refetch` re-runs the request.
export interface AsyncState<T> {
  data: T | null;
  loading: boolean;
  error: SdkError | null;
  refetch: () => void;
}

function useAsync<T>(run: (signal: AbortSignal) => Promise<T>, deps: React.DependencyList): AsyncState<T> {
  const [data, setData] = React.useState<T | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState<SdkError | null>(null);
  const [nonce, setNonce] = React.useState(0);

  React.useEffect(() => {
    const ac = new AbortController();
    setLoading(true);
    setError(null);
    run(ac.signal)
      .then((d) => {
        if (!ac.signal.aborted) {
          setData(d);
          setLoading(false);
        }
      })
      .catch((e: unknown) => {
        if (ac.signal.aborted) return;
        setError(e instanceof SdkError ? e : new SdkError(0, "network", (e as Error)?.message ?? "request failed"));
        setLoading(false);
      });
    return () => ac.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce]);

  const refetch = React.useCallback(() => setNonce((n) => n + 1), []);
  return { data, loading, error, refetch };
}

/** Run an arbitrary query DSL document (wire-shaped; see QueryInput). */
export function useQuery<T = unknown>(doc: QueryInput): AsyncState<QueryResponse<T>> {
  const { client } = useLLMObs();
  const key = JSON.stringify(doc);
  return useAsync((signal) => client.query<T>(doc, signal), [key]);
}

export interface TracesParams {
  from: string;
  to: string;
  filters?: unknown[];
  orderBy?: unknown[];
  limit?: number;
  cursor?: string;
}

/** List traces (target=traces). Convenience wrapper over useQuery. */
export function useTraces(params: TracesParams): AsyncState<QueryResponse<Trace>> {
  const doc = React.useMemo<QueryInput>(
    () => ({
      target: "traces",
      timeRange: { from: params.from, to: params.to },
      ...(params.filters ? { filters: params.filters } : {}),
      ...(params.orderBy ? { orderBy: params.orderBy } : {}),
      ...(params.limit ? { limit: params.limit } : {}),
      ...(params.cursor ? { cursor: params.cursor } : {}),
    }),
    [params.from, params.to, JSON.stringify(params.filters), JSON.stringify(params.orderBy), params.limit, params.cursor],
  );
  return useQuery<Trace>(doc);
}

/** Fetch a full trace tree (trace + spans in tree order). */
export function useTrace(traceId: string | null): AsyncState<TraceTree> {
  const { client } = useLLMObs();
  return useAsync(
    (signal) => (traceId ? client.traceTree(traceId, signal) : Promise.reject(new SdkError(0, "no_id", "no trace id"))),
    [traceId],
  );
}

// MutationState is the shape imperative writes return (no auto-run): call
// `mutate`, watch loading/error, read the result. The `write` primitive.
export interface MutationState<TArg, TResult> {
  mutate: (arg: TArg) => Promise<TResult>;
  loading: boolean;
  error: SdkError | null;
}

// SettingsState is what useSettings returns: the loaded view (values + which
// secrets are set), a save function, and the standard loading/error flags. Pass the
// rendered view straight to <SchemaForm values={data.values} secretsSet={data.secrets} />.
export interface SettingsState {
  data: SettingsView | null;
  loading: boolean;
  error: SdkError | null;
  /** Persist changed values; secrets left out are preserved. Refetches on success. */
  save: (values: Record<string, unknown>) => Promise<void>;
  saving: boolean;
  saveError: SdkError | null;
  refetch: () => void;
}

/**
 * useSettings loads and persists the plugin's settings (J2) through the frontend
 * token. Secret (writeOnly) fields never come back — `data.secrets[name]` only tells
 * you whether one is set. Pair with <SchemaForm> from @llmobs/schema-form.
 */
export function useSettings(): SettingsState {
  const { client } = useLLMObs();
  const { data, loading, error, refetch } = useAsync((signal) => client.getSettings(signal), []);
  const [saving, setSaving] = React.useState(false);
  const [saveError, setSaveError] = React.useState<SdkError | null>(null);
  const save = React.useCallback(
    async (values: Record<string, unknown>) => {
      setSaving(true);
      setSaveError(null);
      try {
        await client.setSettings(values);
        refetch();
      } catch (e: unknown) {
        const err = e instanceof SdkError ? e : new SdkError(0, "network", (e as Error)?.message ?? "save failed");
        setSaveError(err);
        throw err;
      } finally {
        setSaving(false);
      }
    },
    [client, refetch],
  );
  return { data, loading, error, save, saving, saveError, refetch };
}

/** Write a score (the `write` primitive). Requires the scores:write scope. */
export function useWriteScore(): MutationState<ScoreInput, { id: string }> {
  const { client } = useLLMObs();
  const [loading, setLoading] = React.useState(false);
  const [error, setError] = React.useState<SdkError | null>(null);
  const mutate = React.useCallback(
    async (score: ScoreInput) => {
      setLoading(true);
      setError(null);
      try {
        return await client.writeScore(score);
      } catch (e: unknown) {
        const err = e instanceof SdkError ? e : new SdkError(0, "network", (e as Error)?.message ?? "write failed");
        setError(err);
        throw err;
      } finally {
        setLoading(false);
      }
    },
    [client],
  );
  return { mutate, loading, error };
}
