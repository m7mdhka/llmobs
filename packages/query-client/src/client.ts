// Thin fetch wrapper over the Query API (api/openapi/v1alpha1/query.yaml).
// Hand-written (NOT generated); typed with the generated types in types.gen.ts.
// The request/response entity shapes come from the canonical model; the query
// document type is LLMObsQueryDSLDocumentV1Alpha1.
import type {
  LLMObsQueryDSLDocumentV1Alpha1 as QueryDocument,
  LLMObsCanonicalSpanV1Alpha1 as Span,
  LLMObsCanonicalTraceV1Alpha1 as Trace,
  LLMObsCanonicalScoreV1Alpha1 as Score,
} from "./types.gen.js";

/** Response envelope (QD-8). `data` is entities for row queries, group rows for aggregations. */
export interface QueryResponse<T = unknown> {
  version: "v1alpha1";
  data: T[];
  cursor?: string;
  stats: { elapsed_ms: number; scanned?: number; returned?: number; [k: string]: unknown };
  warnings: Array<{ code: string; message: string; detail?: Record<string, unknown> }>;
}

/** Full span tree returned by GET /traces/{id}/tree (QD-10). */
export interface TraceTree {
  trace: Trace;
  spans: Span[];
}

export interface QueryClientOptions {
  /** Base URL of the gateway, e.g. "https://gateway.internal". */
  baseUrl: string;
  /** Kernel-signed service token identifying the plugin (X-LLMObs-Service-Token). */
  serviceToken: string;
  /** Kernel-signed user identity assertion (X-LLMObs-User-Assertion). */
  userAssertion: string;
  /** Optional fetch override (for tests / non-browser runtimes). */
  fetch?: typeof fetch;
}

export class QueryApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
    public readonly detail?: Record<string, unknown>,
  ) {
    super(message);
    this.name = "QueryApiError";
  }
}

/** Minimal, dependency-free client for the Query API. */
export class QueryClient {
  private readonly opts: QueryClientOptions;
  private readonly doFetch: typeof fetch;

  constructor(opts: QueryClientOptions) {
    this.opts = opts;
    this.doFetch = opts.fetch ?? fetch;
  }

  private headers(json: boolean): Record<string, string> {
    const h: Record<string, string> = {
      "X-LLMObs-Service-Token": this.opts.serviceToken,
      "X-LLMObs-User-Assertion": this.opts.userAssertion,
      Accept: "application/json",
    };
    if (json) h["Content-Type"] = "application/json";
    return h;
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const res = await this.doFetch(`${this.opts.baseUrl}${path}`, {
      method,
      headers: this.headers(body !== undefined),
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await res.text();
    const payload = text ? (JSON.parse(text) as unknown) : undefined;
    if (!res.ok) {
      const err = (payload ?? {}) as { code?: string; message?: string; detail?: Record<string, unknown> };
      throw new QueryApiError(res.status, err.code ?? "error", err.message ?? res.statusText, err.detail);
    }
    return payload as T;
  }

  /** Execute a query DSL document (POST /v1alpha1/query). */
  runQuery<T = unknown>(query: QueryDocument): Promise<QueryResponse<T>> {
    return this.request<QueryResponse<T>>("POST", "/v1alpha1/query", query);
  }

  /** Fetch a full trace tree (GET /v1alpha1/traces/{id}/tree). */
  getTraceTree(traceId: string): Promise<TraceTree> {
    return this.request<TraceTree>("GET", `/v1alpha1/traces/${encodeURIComponent(traceId)}/tree`);
  }

  getSpan(id: string): Promise<Span> {
    return this.request<Span>("GET", `/v1alpha1/spans/${encodeURIComponent(id)}`);
  }

  getTrace(id: string): Promise<Trace> {
    return this.request<Trace>("GET", `/v1alpha1/traces/${encodeURIComponent(id)}`);
  }

  getScore(id: string): Promise<Score> {
    return this.request<Score>("GET", `/v1alpha1/scores/${encodeURIComponent(id)}`);
  }

  /** Write a score (POST /v1alpha1/scores) — requires the `write` capability. */
  writeScore(score: Score): Promise<Score> {
    return this.request<Score>("POST", "/v1alpha1/scores", score);
  }
}
