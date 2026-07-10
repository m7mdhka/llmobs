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

/**
 * FrontendTokenProvider yields the current plugin frontend token (J1). The shell
 * supplies this; it caches a per-plugin, per-session, short-TTL token minted by the
 * kernel and refreshes it before expiry. Returning `undefined` means "no token" —
 * the client then falls back to the session cookie (Case 2) unchanged.
 *
 * NOTE — least-privilege-by-default, NOT a boundary. Presenting this token confines
 * a COOPERATING SDK-using frontend to plugin-grant ∩ session ∩ project. It does not
 * contain a hostile frontend: a plugin running in the shell's origin can bypass the
 * SDK and call the gateway with the ambient cookie directly. Hard confinement needs
 * a backend (H3) or origin isolation (future). See docs/plugin-authors/trust-model.
 */
export type FrontendTokenProvider = () => string | undefined | Promise<string | undefined>;

export interface ClientConfig {
  /** Gateway base URL. Empty string = same origin (the shell's origin). */
  baseUrl: string;
  /** Active project id, sent as X-LLMObs-Project. */
  projectId?: string;
  /** Optional fetch override (tests). */
  fetchImpl?: typeof fetch;
  /**
   * Plugin frontend token provider (J1). When set and it yields a token, the
   * client presents it as X-LLMObs-Frontend-Token and the kernel scopes the call
   * to the plugin's least-privilege grant instead of the full user session.
   */
  frontendToken?: FrontendTokenProvider;
}

export class DataClient {
  constructor(private readonly cfg: ClientConfig) {}

  private get doFetch(): typeof fetch {
    // Native fetch must be invoked with `this === window`; a bare reference
    // (`const f = fetch; f()`) throws "Illegal invocation". Bind to globalThis.
    const f = this.cfg.fetchImpl ?? globalThis.fetch;
    return f.bind(globalThis);
  }

  private async headers(json: boolean): Promise<Record<string, string>> {
    const h: Record<string, string> = { Accept: "application/json" };
    if (json) h["Content-Type"] = "application/json";
    if (this.cfg.projectId) h["X-LLMObs-Project"] = this.cfg.projectId;
    // J1: present the plugin frontend token when the shell provides one. The kernel
    // prefers it over the session cookie (Case 1b before Case 2), confining a
    // cooperating frontend to its least-privilege grant.
    if (this.cfg.frontendToken) {
      const tok = await this.cfg.frontendToken();
      if (tok) h["X-LLMObs-Frontend-Token"] = tok;
    }
    return h;
  }

  private async request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
    const res = await this.doFetch(`${this.cfg.baseUrl}${path}`, {
      method,
      headers: await this.headers(body !== undefined),
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

  /**
   * Write a score (the `write` primitive) — closes the read-traces→write-scores
   * loop through the SDK. Requires the scores:write scope (enforced by the kernel
   * intersection). Returns the created score's id.
   */
  writeScore(score: ScoreInput, signal?: AbortSignal): Promise<{ id: string }> {
    return this.request<{ id: string }>("POST", "/v1alpha1/scores", score, signal);
  }

  /**
   * Read the plugin's settings (J2). Authed by the frontend token — the plugin id +
   * project come from the token. Secret (writeOnly) fields are NEVER returned; the
   * `secrets` map only reports whether each secret is set.
   */
  getSettings(signal?: AbortSignal): Promise<SettingsView> {
    return this.request<SettingsView>("POST", "/v1alpha1/plugin/settings/get", {}, signal);
  }

  /**
   * Persist the plugin's settings (J2). Secret fields left out (or blank) are
   * preserved by the kernel. A validation failure surfaces as an SdkError (400).
   */
  setSettings(values: Record<string, unknown>, signal?: AbortSignal): Promise<void> {
    return this.request<void>("POST", "/v1alpha1/plugin/settings/set", { values }, signal);
  }
}

/** The settings `get` response: non-secret values + per-secret "is set" markers. */
export interface SettingsView {
  values: Record<string, unknown>;
  secrets: Record<string, boolean>;
}

// ScoreInput is the wire shape of a score write (LM-3/LM-8): exactly one value_*
// field is set, matching data_type. subject_type is a kernel type
// (span|trace|session) or a namespaced `ns/name`.
export interface ScoreInput {
  id: string;
  subject_type: string;
  subject_id: string;
  name: string;
  data_type: "numeric" | "categorical" | "boolean";
  value_numeric?: number;
  value_categorical?: string;
  value_boolean?: boolean;
  source: string;
  timestamp: string;
  environment: string;
  comment?: string;
  [k: string]: unknown;
}
