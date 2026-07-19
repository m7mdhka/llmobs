import { describe, it, expect } from "vitest";
import { EventsClient, PermanentEventError, type Event } from "../src/events.js";

// A tiny fake transport that records the ops the client issues and lets each test drive
// what poll returns. It mirrors the kernel's /poll, /ack, /fail endpoints closely enough
// to assert the subscribe() taxonomy without a live gateway.
function fakeBus(pollBatches: Event[][]) {
  const calls: Array<{ op: string; body: any }> = [];
  let batch = 0;
  const fetchImpl = (async (url: string, init: any) => {
    const op = String(url).split("/").pop()!;
    const body = JSON.parse(init.body);
    calls.push({ op, body });
    if (op === "poll") {
      const events = pollBatches[batch] ?? [];
      batch = Math.min(batch + 1, pollBatches.length);
      return new Response(JSON.stringify({ events }), { status: 200 });
    }
    return new Response(null, { status: 204 });
  }) as unknown as typeof fetch;

  const client = new EventsClient({ baseUrl: "http://x", authHeaders: () => ({}), fetchImpl });
  return { client, calls };
}

const ev = (id: number, subject: string): Event => ({ id, topic: "span.ingested", project_id: "p", subject_id: subject });

describe("events subscribe taxonomy", () => {
  it("acks a successfully handled event", async () => {
    const { client, calls } = fakeBus([[ev(1, "ok")]]);
    const seen: number[] = [];
    const stop = client.subscribe(["span.ingested"], (e) => {
      seen.push(e.id);
    });
    await waitFor(() => calls.some((c) => c.op === "ack"));
    stop();
    expect(seen).toEqual([1]);
    const ack = calls.find((c) => c.op === "ack");
    expect(ack!.body).toEqual({ topic: "span.ingested", offset: 1 });
    expect(calls.some((c) => c.op === "fail")).toBe(false);
  });

  it("dead-letters a PermanentEventError and continues to the next event", async () => {
    const { client, calls } = fakeBus([[ev(1, "poison"), ev(2, "good")]]);
    const acked: number[] = [];
    const stop = client.subscribe(["span.ingested"], (e) => {
      if (e.subject_id === "poison") throw new PermanentEventError("malformed subject");
      acked.push(e.id);
    });
    await waitFor(() => calls.some((c) => c.op === "fail") && calls.some((c) => c.op === "ack"));
    stop();

    // The poison was failed (not acked); the good event behind it was acked.
    const fail = calls.find((c) => c.op === "fail");
    expect(fail!.body).toEqual({ topic: "span.ingested", id: 1, reason: "malformed subject" });
    expect(acked).toEqual([2]);
  });

  it("treats a plain throw as transient: no fail, event re-delivered next poll", async () => {
    let attempts = 0;
    // Both batches return the same event; a transient throw the first time, success next.
    const { client, calls } = fakeBus([[ev(1, "flaky")], [ev(1, "flaky")]]);
    const stop = client.subscribe(
      ["span.ingested"],
      (e) => {
        attempts++;
        if (attempts === 1) throw new Error("downstream down"); // transient
        // second attempt succeeds
      },
      { intervalMs: 1 },
    );
    await waitFor(() => attempts >= 2 && calls.some((c) => c.op === "ack"));
    stop();

    // A transient failure NEVER dead-letters, and the event is retried (acked on retry).
    expect(calls.some((c) => c.op === "fail")).toBe(false);
    expect(calls.some((c) => c.op === "ack" && c.body.offset === 1)).toBe(true);
  });
});

async function waitFor(cond: () => boolean, timeoutMs = 1000): Promise<void> {
  const start = Date.now();
  while (!cond()) {
    if (Date.now() - start > timeoutMs) throw new Error("waitFor timed out");
    await new Promise((r) => setTimeout(r, 5));
  }
}
