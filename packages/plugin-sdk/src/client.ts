// Same-origin data client for plugin surfaces. Plugins never construct this
// directly — they use the hooks, which read it from the provider context. Reads
// carry the session cookie (credentials) and the active project header; the
// kernel computes the permission intersection. This is the `query` primitive.
import type {
  LLMObsCanonicalSpanV1Alpha1 as Span,
  LLMObsCanonicalTraceV1Alpha1 as Trace,
} from "@llmobs/query-client";

// QueryInput is the JSON-shaped query document plugins actually build: string
// dates and a string target. The *generated* LLMObsQueryDSLDocumentV1Alpha1
// models `timeRange` as `Date` and `target` as an enum (quicktype's date-time
// handling), which is awkward to construct by hand — so the SDK accepts this
// wire-shaped input and lets the kernel validate against dsl.schema.json.
export interface QueryInput {
  target: "spans" | "traces" | "scores";
  timeRange: { from: string; to: string };
  filters?: unknown[];
  orderBy?: unknown[];
  limit?: number;
  cursor?: string;
  [k: string]: unknown;
}

export interface QueryResponse<T = unknown> {
  version: "v1alpha1";
  data: T[];
  cursor?: string;
  stats: { elapsed_ms: number; returned?: number; [k: string]: unknown };
  warnings: Array<{ code: string; message: string }>;
}

export interface TraceTree {
  trace: Trace;
  spans: Span[];
}

export class SdkError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "SdkError";
  }
}

export interface ClientConfig {
  /** Gateway base URL. Empty string = same origin (the shell's origin). */
  baseUrl: string;
  /** Active project id, sent as X-LLMObs-Project. */
  projectId?: string;
  /** Optional fetch override (tests). */
  fetchImpl?: typeof fetch;
}

export class DataClient {
  constructor(private readonly cfg: ClientConfig) {}

  private get doFetch(): typeof fetch {
    return this.cfg.fetchImpl ?? fetch;
  }

  private headers(json: boolean): Record<string, string> {
    const h: Record<string, string> = { Accept: "application/json" };
    if (json) h["Content-Type"] = "application/json";
    if (this.cfg.projectId) h["X-LLMObs-Project"] = this.cfg.projectId;
    return h;
  }

  private async request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
    const res = await this.doFetch(`${this.cfg.baseUrl}${path}`, {
      method,
      headers: this.headers(body !== undefined),
      credentials: "same-origin",
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
    });
    const text = await res.text();
    const payload = text ? JSON.parse(text) : undefined;
    if (!res.ok) {
      const err = (payload ?? {}) as { code?: string; message?: string };
      throw new SdkError(res.status, err.code ?? String(res.status), err.message ?? res.statusText);
    }
    return payload as T;
  }

  /** Run a query DSL document (the `query` primitive). */
  query<T = unknown>(doc: QueryInput, signal?: AbortSignal): Promise<QueryResponse<T>> {
    return this.request<QueryResponse<T>>("POST", "/v1alpha1/query", doc, signal);
  }

  /** Fetch a full trace tree (trace + spans in tree order). */
  traceTree(traceId: string, signal?: AbortSignal): Promise<TraceTree> {
    return this.request<TraceTree>("GET", `/v1alpha1/traces/${encodeURIComponent(traceId)}/tree`, undefined, signal);
  }
}
