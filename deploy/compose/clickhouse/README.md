# ClickHouse (scale profile)

The scale profile adds ClickHouse alongside Postgres (permanent dual-read,
RULING-MIG6). This directory holds its deploy-time config.

## Version support (R-CH5)

- **Pinned image:** `clickhouse/clickhouse-server:24.8` (LTS).
- **Supported range:** 24.3 LTS – 24.8 LTS. Never run `:latest` — a ClickHouse
  minor can change analyzer/DDL behavior the adapter assumes. Bump the pin only
  after the scale e2e passes against the new version.

## System-log retention (R-CH6)

`config.d/system-log-ttl.xml` disables the sampling profiler logs (`trace_log`,
`query_thread_log`) and TTLs the remaining `system.*_log` tables to 14–30 days,
so ClickHouse's own bookkeeping can't silently fill the disk (a Langfuse
self-hosting scar). Adjust the intervals to your retention policy; the invariant
is that no system log is unbounded.

## Grants (R-CH8)

In this single-node compose the bootstrap user self-manages grants
(`CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1`). On a real cluster, grant the LLMObs
user exactly the set returned by `clickhouse.RequiredGrants()`:

    GRANT CREATE TABLE, INSERT, SELECT, ALTER, OPTIMIZE, DROP TABLE ON llmobs.* TO <llmobs_user>

The adapter runs a functional preflight (`clickhouse.Preflight`) at startup that
fails early naming any missing privilege.

## Credentials in the DSN (R-CH7)

Build the connection DSN via `clickhouse.BuildDSN`, which URL-safe-encodes the
username and password. Passwords with `@ : / #` corrupt a hand-written DSN's
authority; never paste raw credentials into `LLMOBS_CLICKHOUSE_URL`.
