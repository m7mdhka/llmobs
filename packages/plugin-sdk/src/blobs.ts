// The `blobs` primitive — kernel-brokered large-artifact storage (a BACKEND primitive,
// double token). Objects are tenant-scoped by the kernel from the caller's (plugin,
// project): you address a blob by a logical key you choose, and the kernel derives a
// physical key that no other plugin or project can reach. Use this for large binary
// artifacts (exported reports, model files, attachments) — kv is for small values.
import { SdkError } from "./client.js";

export interface BlobsConfig {
  baseUrl: string;
  authHeaders: () => Record<string, string>;
  fetchImpl?: typeof fetch;
}

export interface BlobInfo {
  contentType: string;
  size: number | null;
}

export class BlobsClient {
  constructor(private readonly cfg: BlobsConfig) {}

  private url(op: string, key: string): string {
    return `${this.cfg.baseUrl}/v1alpha1/plugin/blobs/${op}?key=${encodeURIComponent(key)}`;
  }

  private get fetch(): typeof fetch {
    return this.cfg.fetchImpl ?? fetch;
  }

  /** Store bytes under a logical key, overwriting any existing object. */
  async put(key: string, body: Uint8Array | Blob | ArrayBuffer, contentType = "application/octet-stream"): Promise<void> {
    const res = await this.fetch(this.url("put", key), {
      method: "POST",
      headers: { "Content-Type": contentType, ...this.cfg.authHeaders() },
      body: body instanceof ArrayBuffer ? new Uint8Array(body) : body,
    });
    if (res.status !== 204) throw await bErr(res);
  }

  /** Fetch a blob's bytes. Returns null if the key does not exist. */
  async get(key: string): Promise<{ bytes: Uint8Array; info: BlobInfo } | null> {
    const res = await this.fetch(this.url("get", key), {
      method: "POST",
      headers: { ...this.cfg.authHeaders() },
    });
    if (res.status === 404) return null;
    if (!res.ok) throw await bErr(res);
    const buf = new Uint8Array(await res.arrayBuffer());
    const len = res.headers.get("Content-Length");
    return {
      bytes: buf,
      info: { contentType: res.headers.get("Content-Type") ?? "application/octet-stream", size: len ? Number(len) : buf.byteLength },
    };
  }

  /** Delete a blob. Deleting a missing key is not an error (idempotent). */
  async delete(key: string): Promise<void> {
    const res = await this.fetch(this.url("delete", key), {
      method: "POST",
      headers: { ...this.cfg.authHeaders() },
    });
    if (res.status !== 204) throw await bErr(res);
  }
}

async function bErr(res: Response): Promise<SdkError> {
  let code = "blobs_error";
  try {
    code = ((await res.json()) as { error?: string }).error ?? code;
  } catch {
    /* non-JSON (e.g. a streamed body) */
  }
  return new SdkError(res.status, code, `blobs operation failed: ${code}`);
}
