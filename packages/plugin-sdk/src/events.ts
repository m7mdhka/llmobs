// The `events` primitive — durable subscribe over the Postgres-backed bus (H6). A
// BACKEND primitive (double token). Delivery is AT-LEAST-ONCE: each event carries
// a monotonic `id` that is the idempotency key — the caller dedupes on it. A
// subscriber that was down REPLAYS its backlog from its offset on the next poll.
// (The lite bus is Postgres; Redis Streams is the deferred scale backend behind
// the same contract — this client is unchanged across both.)
import { SdkError } from "./client.js";

export interface EventsConfig {
  baseUrl: string;
  authHeaders: () => Record<string, string>;
  fetchImpl?: typeof fetch;
}

export interface Event {
  id: number; // monotonic offset + idempotency key
  topic: string;
  project_id: string;
  subject_id: string; // fetch the entity via the query primitive
}

export class EventsClient {
  constructor(private readonly cfg: EventsConfig) {}

  private call(op: string, body: unknown): Promise<Response> {
    const f = this.cfg.fetchImpl ?? fetch;
    return f(`${this.cfg.baseUrl}/v1alpha1/plugin/events/${op}`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...this.cfg.authHeaders() },
      body: JSON.stringify(body),
    });
  }

  /** Poll for events across topics (does NOT advance the offset — ack does). */
  async poll(topics: string[], max = 100): Promise<Event[]> {
    const res = await this.call("poll", { topics, max });
    if (!res.ok) throw await eErr(res);
    return (await res.json()).events as Event[];
  }

  /** Ack a topic up to (and including) an event id — stops re-delivery. */
  async ack(topic: string, offset: number): Promise<void> {
    const res = await this.call("ack", { topic, offset });
    if (!res.ok && res.status !== 204) throw await eErr(res);
  }

  /**
   * Dead-letter ONE event this subscriber can never process — a PERMANENT failure
   * (malformed subject, a deterministic handler rejection). The event is recorded to
   * the dead-letter log and the offset advances past it, so the good events queued
   * behind it flow on the next poll.
   *
   * Use this ONLY for permanent failures. For a TRANSIENT failure (your downstream is
   * momentarily down), do NOT ack and do NOT fail — the event is re-delivered on the
   * next poll and is never dropped. `id` must be the head of your unacked window (the
   * lowest un-acked id): ack the good events before the poison first, else the kernel
   * refuses with a 409.
   */
  async fail(topic: string, id: number, reason: string): Promise<void> {
    const res = await this.call("fail", { topic, id, reason });
    if (!res.ok && res.status !== 204) throw await eErr(res);
  }

  /**
   * Run a poll→handle→ack loop until stopped. The handler is invoked once per event
   * in id order; because delivery is at-least-once the handler MUST be idempotent (it
   * receives the event id to dedupe). Returns a stop() function.
   *
   * The handler signals the permanent-vs-transient taxonomy by how it fails:
   *   - returns normally  → success; the event is acked.
   *   - throws PermanentEventError → the event is dead-lettered (fail) and skipped;
   *     the loop continues with the events behind it.
   *   - throws anything else → treated as TRANSIENT: the batch stops at that event and
   *     it is re-delivered on the next poll (never dropped). Ack/fail happen per event,
   *     so a permanent poison never blocks the good events queued behind it.
   */
  subscribe(topics: string[], handler: (e: Event) => Promise<void> | void, opts: { intervalMs?: number } = {}): () => void {
    let stopped = false;
    const interval = opts.intervalMs ?? 1000;
    const loop = async () => {
      while (!stopped) {
        let progressed = false;
        try {
          const events = await this.poll(topics);
          for (const e of events) {
            if (stopped) break;
            try {
              await handler(e);
              await this.ack(e.topic, e.id);
              progressed = true;
            } catch (err) {
              if (err instanceof PermanentEventError) {
                // Permanent: dead-letter this one and advance past it. Because we ack
                // each event as it succeeds, this event is the head of the unacked
                // window, so fail() is accepted.
                await this.fail(e.topic, e.id, err.reason);
                progressed = true;
                continue;
              }
              // Transient: stop the batch here. The offset sits at the last acked event,
              // so this one is re-delivered on the next poll — retried, never dropped.
              break;
            }
          }
        } catch {
          /* poll failed; back off below */
        }
        if (!progressed) await sleep(interval);
      }
    };
    void loop();
    return () => {
      stopped = true;
    };
  }
}

/**
 * Throw this from a subscribe() handler to mark an event as a PERMANENT failure — one
 * that can never succeed for this subscriber (a malformed subject, a deterministic
 * rejection). The event is dead-lettered and skipped. For a transient failure, throw
 * any other error (or let one propagate) and the event is retried on the next poll.
 */
export class PermanentEventError extends Error {
  constructor(public readonly reason: string) {
    super(`permanent event failure: ${reason}`);
    this.name = "PermanentEventError";
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

async function eErr(res: Response): Promise<SdkError> {
  let code = "events_error";
  try {
    code = ((await res.json()) as { error?: string }).error ?? code;
  } catch {
    /* non-JSON */
  }
  return new SdkError(res.status, code, `events operation failed: ${code}`);
}
