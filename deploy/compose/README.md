# deploy/compose — lite profile

The lite deployment: `docker compose up`, Postgres-only, first-party plugins in
the shared runtime (`runtime: shared`). Target: 200 spans/s on 2 vCPU / 4 GB
(D13).

- A **base** compose file defines the kernel, Postgres, and the web shell.
- **Per-plugin overlays** are generated (e.g. by `llmobs add` / the `new-plugin`
  flow) and merged on top of the base.

This is what `make dev` and the `e2e-compose` CI job bring up.
