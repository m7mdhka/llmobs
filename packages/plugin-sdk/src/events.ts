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
   * Run a poll→handle→ack loop until stopped. The handler is invoked once per
   * event; because delivery is at-least-once the handler MUST be idempotent (it
   * receives the event id to dedupe). Returns a stop() function.
   */
  subscribe(topics: string[], handler: (e: Event) => Promise<void> | void, opts: { intervalMs?: number } = {}): () => void {
    let stopped = false;
    const interval = opts.intervalMs ?? 1000;
    const loop = async () => {
      while (!stopped) {
        try {
          const events = await this.poll(topics);
          // Group the max acked id per topic so one ack advances the whole batch.
          const maxByTopic = new Map<string, number>();
          for (const e of events) {
            await handler(e);
            maxByTopic.set(e.topic, Math.max(maxByTopic.get(e.topic) ?? 0, e.id));
          }
          for (const [topic, id] of maxByTopic) await this.ack(topic, id);
          if (events.length === 0) await sleep(interval);
        } catch {
          await sleep(interval);
        }
      }
    };
    void loop();
    return () => {
      stopped = true;
    };
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
