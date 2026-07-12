// The `kv` primitive — a per-plugin, tenant-scoped key/value store (H4). It is a
// BACKEND primitive: calls carry the plugin's double token (service token +
// forwarded identity assertion), so the kernel scopes every operation to
// (plugin_id, project_id) and gates on the kv capability. A plugin never touches
// the database.
//
// Scope (Arc O / O6): each call is "project" (default — shared across the project's
// users) or "user" (per-user, isolated). For USER scope the kernel keys on the
// caller's server-resolved identity (from the verified assertion) — never a value the
// plugin supplies — so a plugin can store per-user config (dashboard prefs, saved
// views) that one user cannot read or overwrite for another, even in the same project.
import { SdkError } from "./client.js";

/** "project" = shared across the project's users; "user" = per-user, isolated. */
export type KvScope = "project" | "user";

export interface KvConfig {
  /** Kernel base URL. */
  baseUrl: string;
  /** Returns the auth headers for a plugin→kernel call (service token + assertion). */
  authHeaders: () => Record<string, string>;
  fetchImpl?: typeof fetch;
}

export interface KvOpts {
  /** Storage scope; defaults to "project". */
  scope?: KvScope;
}

export class KvClient {
  constructor(private readonly cfg: KvConfig) {}

  private async call(op: string, body: unknown): Promise<Response> {
    const f = this.cfg.fetchImpl ?? fetch;
    return f(`${this.cfg.baseUrl}/v1alpha1/plugin/kv/${op}`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...this.cfg.authHeaders() },
      body: JSON.stringify(body),
    });
  }

  /** Get a value, or undefined if the key is absent. */
  async get<T = unknown>(key: string, opts?: KvOpts): Promise<T | undefined> {
    const res = await this.call("get", { key, scope: opts?.scope });
    if (res.status === 404) return undefined;
    if (!res.ok) throw await sdkErr(res);
    return (await res.json()).value as T;
  }

  /** Set a value (JSON-serializable). */
  async set(key: string, value: unknown, opts?: KvOpts): Promise<void> {
    const res = await this.call("set", { key, value, scope: opts?.scope });
    if (!res.ok && res.status !== 204) throw await sdkErr(res);
  }

  /** Delete a key (no-op if absent). */
  async delete(key: string, opts?: KvOpts): Promise<void> {
    const res = await this.call("delete", { key, scope: opts?.scope });
    if (!res.ok && res.status !== 204) throw await sdkErr(res);
  }

  /** List keys with an optional prefix. */
  async list(prefix = "", opts?: KvOpts): Promise<string[]> {
    const res = await this.call("list", { prefix, scope: opts?.scope });
    if (!res.ok) throw await sdkErr(res);
    return (await res.json()).keys as string[];
  }
}

async function sdkErr(res: Response): Promise<SdkError> {
  let code = "kv_error";
  try {
    code = ((await res.json()) as { error?: string }).error ?? code;
  } catch {
    /* non-JSON body */
  }
  return new SdkError(res.status, code, `kv operation failed: ${code}`);
}
