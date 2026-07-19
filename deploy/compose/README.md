# deploy/compose — lite profile

The lite deployment: `docker compose up`, Postgres-only, first-party plugins in
the shared runtime (`runtime: shared`). Target: 200 spans/s on 2 vCPU / 4 GB
(D13).

- A **base** compose file defines the kernel, Postgres, and the web shell.
- **Per-plugin overlays** are generated (e.g. by `llmobs add` / the `new-plugin`
  flow) and merged on top of the base.

This is what `make dev` and the `e2e-compose` CI job bring up.

## Credentials are required, never defaulted

Every credential in the compose files (`POSTGRES_PASSWORD`, `CLICKHOUSE_PASSWORD`,
the datastore DSNs, `LLMOBS_BOOTSTRAP_API_KEY`, `LLMOBS_BOOTSTRAP_ADMIN_PASSWORD`)
is referenced as `${VAR:?...}` — **required, with no inline default**. Copying a
compose file to a real host and running it without supplying these values fails at
parse time with a clear message, rather than silently booting with a password an
attacker already knows.

For local dev and CI, `deploy/compose/dev.env` carries throwaway dev-only values
and is loaded explicitly:

```sh
docker compose --env-file deploy/compose/dev.env -f deploy/compose/lite.yaml up -d
```

`make dev`, `make e2e`, and the plugin/web e2e scripts already pass `--env-file
deploy/compose/dev.env`. A production deployment supplies these from its own secret
store or `--env-file`, never from `dev.env`.
