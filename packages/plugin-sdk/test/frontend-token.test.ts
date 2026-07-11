// G1: the SDK DataClient must FAIL CLOSED in a plugin context. When a frontend-token
// provider is configured (i.e. the client runs inside a plugin surface), every kernel
// call MUST carry the plugin-frontend marker + a token, and if a token cannot be
// obtained the call MUST throw rather than fall back to the ambient session cookie —
// which the kernel would scope to the user's FULL permissions (the G1 escalation).
import * as React from "react";
import { describe, it, expect, vi } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { DataClient, SdkError } from "../dist/index.js";
import { LLMObsPluginProvider, useLLMObs } from "../dist/react.js";

function fakeFetch(captured: { headers?: Record<string, string> }) {
  return vi.fn(async (_url: string, init: RequestInit) => {
    captured.headers = init.headers as Record<string, string>;
    return new Response(JSON.stringify({ version: "v1alpha1", data: [], stats: {}, warnings: [] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
}

describe("G1 SDK frontend-token fail-closed", () => {
  it("attaches the plugin-frontend marker AND the token when the provider yields one", async () => {
    const captured: { headers?: Record<string, string> } = {};
    const fetchImpl = fakeFetch(captured);
    const client = new DataClient({
      baseUrl: "",
      frontendToken: async () => "tok-123",
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });
    await client.query({ target: "traces", timeRange: { from: "2026-01-01T00:00:00Z", to: "2026-01-02T00:00:00Z" } });
    expect(fetchImpl).toHaveBeenCalledOnce();
    expect(captured.headers?.["X-LLMObs-Plugin-Frontend"]).toBe("1");
    expect(captured.headers?.["X-LLMObs-Frontend-Token"]).toBe("tok-123");
  });

  it("THROWS and never calls the kernel when the provider yields no token (fail closed)", async () => {
    const captured: { headers?: Record<string, string> } = {};
    const fetchImpl = fakeFetch(captured);
    const client = new DataClient({
      baseUrl: "",
      frontendToken: async () => undefined, // mint failed / dropped
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });
    await expect(
      client.query({ target: "traces", timeRange: { from: "2026-01-01T00:00:00Z", to: "2026-01-02T00:00:00Z" } }),
    ).rejects.toMatchObject({ code: "frontend_token_unavailable" });
    // The critical assertion: NO request reached the kernel with only the session cookie.
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("SdkError shape is preserved for the fail-closed error", async () => {
    const client = new DataClient({ baseUrl: "", frontendToken: async () => undefined, fetchImpl: (async () => new Response("{}")) as unknown as typeof fetch });
    await client
      .query({ target: "traces", timeRange: { from: "2026-01-01T00:00:00Z", to: "2026-01-02T00:00:00Z" } })
      .then(() => {
        throw new Error("should have thrown");
      })
      .catch((e: unknown) => {
        expect(e).toBeInstanceOf(SdkError);
        expect((e as SdkError).status).toBe(401);
      });
  });

  it("a non-plugin client (no provider) sends neither header (the shell's own calls are unaffected)", async () => {
    const captured: { headers?: Record<string, string> } = {};
    const fetchImpl = fakeFetch(captured);
    const client = new DataClient({ baseUrl: "", fetchImpl: fetchImpl as unknown as typeof fetch });
    await client.query({ target: "traces", timeRange: { from: "2026-01-01T00:00:00Z", to: "2026-01-02T00:00:00Z" } });
    expect(captured.headers?.["X-LLMObs-Plugin-Frontend"]).toBeUndefined();
    expect(captured.headers?.["X-LLMObs-Frontend-Token"]).toBeUndefined();
  });
});

// The G1 shell-mount-seam guard: a plugin surface mounted through LLMObsPluginProvider
// WITHOUT a frontend-token provider (a forgotten mint wiring) must still fail closed —
// the constructed DataClient defaults to a no-token provider, so calls throw rather than
// run at the ambient session's full scope.
import { LLMObsPluginProvider, useLLMObs } from "../dist/react.js";
import * as React from "react";
import { render, waitFor } from "@testing-library/react";

describe("G1 shell-mount-seam guard", () => {
  it("a provider-less plugin mount yields a fail-closed client (no full-scope fallback)", async () => {
    const fetchImpl = vi.fn(async () => new Response("{}", { status: 200 }));
    let result: string | null = null;
    function Probe(): React.ReactElement {
      const { client } = useLLMObs();
      React.useEffect(() => {
        client
          .query({ target: "traces", timeRange: { from: "2026-01-01T00:00:00Z", to: "2026-01-02T00:00:00Z" } })
          .then(() => (result = "UNCONFINED"))
          .catch((e: any) => (result = e.code ?? "err"));
      }, [client]);
      return React.createElement("div");
    }
    render(
      React.createElement(
        LLMObsPluginProvider,
        { config: { baseUrl: "", fetchImpl: fetchImpl as unknown as typeof fetch } },
        React.createElement(Probe),
      ),
    );
    await waitFor(() => expect(result).not.toBeNull());
    expect(result).toBe("frontend_token_unavailable");
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});
