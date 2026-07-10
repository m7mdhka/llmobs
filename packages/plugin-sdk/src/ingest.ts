// The `ingest` capability (H7) — a compat plugin pushes OTLP-format spans through
// the kernel's SAME pipeline as native OTLP. A BACKEND capability (double token).
// The kernel stamps source = plugin:{id} (a plugin cannot forge it) and scopes
// every span to the caller's project (a plugin cannot write outside its tenant).
import { SdkError } from "./client.js";

export interface IngestConfig {
  baseUrl: string;
  authHeaders: () => Record<string, string>;
  fetchImpl?: typeof fetch;
}

export class IngestClient {
  constructor(private readonly cfg: IngestConfig) {}

  /**
   * Push OTLP/JSON (or protobuf) trace bytes to the kernel. `body` is a serialized
   * OTLP ExportTraceServiceRequest; `contentType` defaults to application/json.
   */
  async traces(body: string | Uint8Array, contentType = "application/json"): Promise<void> {
    const f = this.cfg.fetchImpl ?? fetch;
    const res = await f(`${this.cfg.baseUrl}/v1alpha1/plugin/ingest/traces`, {
      method: "POST",
      headers: { "Content-Type": contentType, ...this.cfg.authHeaders() },
      body,
    });
    if (!res.ok && res.status !== 202) {
      let code = "ingest_error";
      try {
        code = ((await res.json()) as { error?: string }).error ?? code;
      } catch {
        /* non-JSON */
      }
      throw new SdkError(res.status, code, `ingest failed: ${code}`);
    }
  }
}
