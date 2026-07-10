-- Plugin job runs (Arc H / H6b, the `jobs` primitive). The audit trail of
-- job-initiated activity: every scheduled or on-demand run records who/what
-- triggered it (actor = 'system:job:...' for scheduled, 'session:...' for
-- on-demand), so scheduled-vs-on-behalf-of-user is always answerable (pin 2).
-- Postgres-backed, advisory-lock leader-elected — zero new infrastructure.
CREATE TABLE IF NOT EXISTS plugin_job_runs (
    id          TEXT PRIMARY KEY,
    plugin_id   TEXT NOT NULL,
    job         TEXT NOT NULL,
    trigger     TEXT NOT NULL,          -- 'schedule' | 'on_demand'
    actor       TEXT NOT NULL,          -- system-initiated vs on-behalf-of-user
    status      TEXT NOT NULL,          -- 'running' | 'succeeded' | 'failed'
    attempts    INTEGER NOT NULL DEFAULT 0,
    error       TEXT,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_job_runs_plugin ON plugin_job_runs (plugin_id, job, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_job_runs_running ON plugin_job_runs (plugin_id, job) WHERE status = 'running';
