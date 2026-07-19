import { describe, it, expect } from "vitest";
import { BlobsClient } from "../src/blobs.js";

// A fake object store keyed by the ?key= query param, exercising put/get/delete.
function fakeStore() {
  const objects = new Map<string, { body: string; ct: string }>();
  const fetchImpl = (async (url: string, init: any) => {
    const u = new URL(url, "http://x");
    const op = u.pathname.split("/").pop()!;
    const key = u.searchParams.get("key")!;
    if (op === "put") {
      const body = typeof init.body === "string" ? init.body : new TextDecoder().decode(init.body);
      objects.set(key, { body, ct: init.headers["Content-Type"] });
      return new Response(null, { status: 204 });
    }
    if (op === "get") {
      const o = objects.get(key);
      if (!o) return new Response(JSON.stringify({ error: "not found" }), { status: 404 });
      return new Response(o.body, { status: 200, headers: { "Content-Type": o.ct, "Content-Length": String(o.body.length) } });
    }
    if (op === "delete") {
      objects.delete(key);
      return new Response(null, { status: 204 });
    }
    return new Response(null, { status: 500 });
  }) as unknown as typeof fetch;
  const client = new BlobsClient({ baseUrl: "http://x", authHeaders: () => ({}), fetchImpl });
  return { client, objects };
}

const enc = (s: string) => new TextEncoder().encode(s);
const dec = (b: Uint8Array) => new TextDecoder().decode(b);

describe("BlobsClient", () => {
  it("round-trips put/get/delete", async () => {
    const { client } = fakeStore();
    await client.put("report.json", enc(`{"ok":true}`), "application/json");

    const got = await client.get("report.json");
    expect(got).not.toBeNull();
    expect(dec(got!.bytes)).toBe(`{"ok":true}`);
    expect(got!.info.contentType).toBe("application/json");

    await client.delete("report.json");
    expect(await client.get("report.json")).toBeNull();
  });

  it("returns null for a missing key", async () => {
    const { client } = fakeStore();
    expect(await client.get("absent")).toBeNull();
  });

  it("URL-encodes the logical key", async () => {
    const { client, objects } = fakeStore();
    await client.put("a/b c", enc("x"));
    // The store is keyed by the decoded key, so a slash/space round-trips through encoding.
    expect(objects.has("a/b c")).toBe(true);
  });
});
