// The `secrets` primitive — per-plugin encrypted secret store (H4). A BACKEND
// primitive (double token). The plaintext is returned by exactly one path: the
// owning plugin's authenticated `get` (delivery, to use the secret). `list`
// returns names + set/unset flags only; no other read path returns a value.
import { SdkError } from "./client.js";

export interface SecretsConfig {
  baseUrl: string;
  authHeaders: () => Record<string, string>;
  fetchImpl?: typeof fetch;
}

export interface SecretInfo {
  name: string;
  set: boolean;
}

export class SecretsClient {
  constructor(private readonly cfg: SecretsConfig) {}

  private call(op: string, body: unknown): Promise<Response> {
    const f = this.cfg.fetchImpl ?? fetch;
    return f(`${this.cfg.baseUrl}/v1alpha1/plugin/secrets/${op}`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...this.cfg.authHeaders() },
      body: JSON.stringify(body),
    });
  }

  /** Set (or replace) a secret value. */
  async set(name: string, value: string): Promise<void> {
    const res = await this.call("set", { name, value });
    if (!res.ok && res.status !== 204) throw await sErr(res);
  }

  /**
   * Deliver a secret's plaintext to THIS plugin (the only value-returning path).
   * Returns undefined if the secret is not set.
   */
  async get(name: string): Promise<string | undefined> {
    const res = await this.call("get", { name });
    if (res.status === 404) return undefined;
    if (!res.ok) throw await sErr(res);
    return (await res.json()).value as string;
  }

  /** List secret names + whether each is set — never values. */
  async list(): Promise<SecretInfo[]> {
    const res = await this.call("list", {});
    if (!res.ok) throw await sErr(res);
    return (await res.json()).secrets as SecretInfo[];
  }

  /** Delete a secret. */
  async delete(name: string): Promise<void> {
    const res = await this.call("delete", { name });
    if (!res.ok && res.status !== 204) throw await sErr(res);
  }
}

async function sErr(res: Response): Promise<SdkError> {
  let code = "secrets_error";
  try {
    code = ((await res.json()) as { error?: string }).error ?? code;
  } catch {
    /* non-JSON */
  }
  return new SdkError(res.status, code, `secrets operation failed: ${code}`);
}
