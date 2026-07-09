# tools/loadgen

Synthetic OpenTelemetry GenAI traffic generator (D13). Emits spans across
dialects at configurable rates against a running instance.

Three jobs, one tool:

1. **Perf gate** — drives the `perf` CI workflow against the D13 targets (lite:
   200 spans/s; scale: 10k spans/s per ingestion worker). A regression fails CI.
2. **e2e smoke** — used by `e2e-compose` to prove a trace is visible end-to-end.
3. **Demo-data seeder** — populates a local instance for development/demos.

All generated data is synthetic — safe to record as conformance fixtures.
