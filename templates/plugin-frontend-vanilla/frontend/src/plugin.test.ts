import { describe, it, expect, vi } from "vitest";
import { DataClient, type LLMObsClient, type PluginMountContext } from "@llmobs/plugin-sdk";
import { mount } from "./plugin.js";

// The Arc N falsification test (H7-equivalent for "any framework"): this file drives the
// vanilla plugin through the NEUTRAL contract with ZERO React in scope — proving the
// contract is not React-shaped — and proves the G1 frontend-token boundary holds
// identically for a non-React caller.

function ctx(client: LLMObsClient, over: Partial<PluginMountContext> = {}): PluginMountContext {
  return {
    baseUrl: "",
    basePath: "/vanilla",
    client,
    theme: { mode: "light" },
    locale: "en",
    direction: "ltr",
    ...over,
  };
}

// A microtask flush that also drains the DataClient's async header build + rejection.
async function flush(): Promise<void> {
  for (let i = 0; i < 5; i++) await Promise.resolve();
}

describe("vanilla plugin mount — framework-neutral proof", () => {
  it("mounts, queries via the neutral client, renders, and unmounts (no framework)", async () => {
    const query = vi.fn(async () => ({
      version: "v1alpha1" as const,
      data: [{ id: "t1", name: "trace one" }],
      stats: { elapsed_ms: 1 },
      warnings: [],
    }));
    const fake = {
      query,
      traceTree: vi.fn(),
      writeScore: vi.fn(),
      getSettings: vi.fn(),
      setSettings: vi.fn(),
    } as unknown as LLMObsClient;

    const el = document.createElement("div");
    const unmount = mount(el, ctx(fake));
    await flush();

    expect(query).toHaveBeenCalledTimes(1);
    expect(el.querySelector(".trace-list")?.children.length).toBe(1);
    expect(el.textContent).toContain("trace one");

    // Teardown removes every node the plugin added.
    (unmount as () => void)();
    expect(el.children.length).toBe(0);
  });

  it("reads locale + direction (N3): renders RTL and localized strings for an Arabic locale", async () => {
    const fake = {
      query: async () => ({ version: "v1alpha1" as const, data: [], stats: { elapsed_ms: 1 }, warnings: [] }),
      traceTree: () => {}, writeScore: () => {}, getSettings: () => {}, setSettings: () => {},
    } as unknown as LLMObsClient;
    const el = document.createElement("div");
    const unmount = mount(el, ctx(fake, { locale: "ar-EG", direction: "rtl" }));
    await flush();

    const section = el.querySelector(".vanilla-plugin") as HTMLElement;
    // Layout flips: the plugin stamps the shell-supplied direction on its root.
    expect(section.getAttribute("dir")).toBe("rtl");
    // The i18n seam resolves the Arabic string (via the primary "ar" catalog) — not the
    // English source fallback.
    expect(el.querySelector("h1")?.textContent).toContain("عمليات التتبع");
    (unmount as () => void)();
  });

  it("PROVE-THE-NEGATIVE: G1 confines a non-React caller identically — a fail-closed client throws before the network, no data reaches the surface", async () => {
    // A REAL DataClient whose token provider yields nothing — the exact fail-closed path a
    // React plugin hits. fetch must NEVER be called: the client refuses at header-build.
    const fetchImpl = vi.fn();
    const noToken = new DataClient({
      baseUrl: "",
      frontendToken: async () => undefined,
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    const el = document.createElement("div");
    const unmount = mount(el, ctx(noToken));
    await flush();

    const status = el.querySelector(".status");
    expect(status?.getAttribute("data-error")).toBe("frontend_token_unavailable");
    // The confinement fired BEFORE any network call — a non-React caller cannot bypass it.
    expect(fetchImpl).not.toHaveBeenCalled();
    expect(el.querySelector(".trace-list")?.children.length).toBe(0);

    (unmount as () => void)();
  });
});
