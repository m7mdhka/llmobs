// The `store` primitive — plugin-owned structured collections (H5, the eighth
// primitive). A BACKEND primitive (double token): the kernel scopes every op to
// (plugin_id, project_id) and gates on cap:store. The plugin never touches the
// database. Collections are declared in the manifest (spec.store); the kernel
// provisions them during the plugin's starting phase.
//
// IMPORTANT: `store` holds plugin DOMAIN entities (datasets, prompt versions,
// dashboards, …). It MUST NOT duplicate telemetry — the canonical model
// (spans/traces/scores, reached via `query`) is the single source of truth.
import { SdkError } from "./client.js";

export interface StoreConfig {
  baseUrl: string;
  authHeaders: () => Record<string, string>;
  fetchImpl?: typeof fetch;
}

export type StoreOp = "eq" | "ne" | "lt" | "lte" | "gt" | "gte";

export interface StoreFilter {
  field: string; // must be a declared indexed field
  op: StoreOp;
  value: unknown;
}

export interface StoreQuery {
  filters?: StoreFilter[];
  order?: { field: string; desc?: boolean }; // indexed field
  limit?: number;
  cursor?: string;
}

export interface StorePage<T> {
  data: T[];
  cursor: string; // "" when exhausted
}

export class StoreClient {
  constructor(private readonly cfg: StoreConfig) {}

  private call(op: string, body: unknown): Promise<Response> {
    const f = this.cfg.fetchImpl ?? fetch;
    return f(`${this.cfg.baseUrl}/v1alpha1/plugin/store/${op}`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...this.cfg.authHeaders() },
      body: JSON.stringify(body),
    });
  }

  /** Upsert a record by id into a collection. */
  async put(collection: string, id: string, record: unknown): Promise<void> {
    const res = await this.call("put", { collection, id, record });
    if (!res.ok && res.status !== 204) throw await stErr(res);
  }

  /** Get a record by id, or undefined. */
  async get<T = unknown>(collection: string, id: string): Promise<T | undefined> {
    const res = await this.call("get", { collection, id });
    if (res.status === 404) return undefined;
    if (!res.ok) throw await stErr(res);
    return (await res.json()).record as T;
  }

  /** Query a collection (filter/order/paginate on declared indexed fields). */
  async query<T = unknown>(collection: string, q: StoreQuery = {}): Promise<StorePage<T>> {
    const res = await this.call("query", { collection, query: q });
    if (!res.ok) throw await stErr(res);
    const body = (await res.json()) as { data: T[]; cursor: string };
    return { data: body.data, cursor: body.cursor };
  }

  /** Delete a record by id. */
  async delete(collection: string, id: string): Promise<void> {
    const res = await this.call("delete", { collection, id });
    if (!res.ok && res.status !== 204) throw await stErr(res);
  }
}

async function stErr(res: Response): Promise<SdkError> {
  let code = "store_error";
  try {
    code = ((await res.json()) as { error?: string }).error ?? code;
  } catch {
    /* non-JSON */
  }
  return new SdkError(res.status, code, `store operation failed: ${code}`);
}
