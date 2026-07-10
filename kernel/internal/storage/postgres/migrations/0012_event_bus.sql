-- Durable event bus (Arc H / H6, the `events` primitive) — Postgres-backed for the
-- lite profile: NO new infrastructure (single-dependency, Postgres-only). Durability
-- is the log + offsets below; LISTEN/NOTIFY is only a latency wake. Redis Streams is
-- the deferred scale backend behind the same bus interface.

-- The durable event log. id is the monotonic offset + idempotency key.
CREATE TABLE IF NOT EXISTS plugin_event_log (
    id         BIGSERIAL PRIMARY KEY,
    topic      TEXT NOT NULL,
    project_id TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_event_log_topic_proj ON plugin_event_log (topic, project_id, id);

-- Per-subscriber consumer offsets (last acked event id per plugin+project+topic).
CREATE TABLE IF NOT EXISTS plugin_event_offsets (
    plugin_id  TEXT NOT NULL,
    project_id TEXT NOT NULL,
    topic      TEXT NOT NULL,
    offset_id  BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (plugin_id, project_id, topic)
);

-- Dead-letter records: an event range a subscriber fell too far behind to receive
-- (backlog cap exceeded), so a dead subscriber cannot pin the log forever.
CREATE TABLE IF NOT EXISTS plugin_event_dlq (
    plugin_id  TEXT NOT NULL,
    project_id TEXT NOT NULL,
    topic      TEXT NOT NULL,
    from_id    BIGINT NOT NULL,
    to_id      BIGINT NOT NULL,
    reason     TEXT NOT NULL,
    dead_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_event_dlq_plugin ON plugin_event_dlq (plugin_id, project_id, topic);
