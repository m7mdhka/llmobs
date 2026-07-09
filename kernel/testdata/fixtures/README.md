# SDK conformance fixtures

Recorded ingestion traffic, one directory per dialect, replayed through the
pipeline in CI to assert canonical output (D3). This is the "verified" bar for
dialect support: a normalizer change is proven by a fixture.

## Rules

- **Synthetic only.** Fixtures never contain real user prompt/completion
  payloads or any real user data. Generate them with `tools/loadgen` or
  hand-author representative spans.
- **One directory per dialect.** `otel-genai/`, `openinference/`,
  `openllmetry/`, `langfuse/` (add more as dialects are supported).
- Each fixture pairs the **input** (raw dialect payload) with the **expected
  canonical output** so the conformance harness can assert the mapping.

## Recording procedure

1. Produce representative traffic for the dialect with `tools/loadgen` (or a
   minimal instrumented sample app under `examples/`), pointed at a local lite
   instance.
2. Capture the raw payloads the receiver sees; scrub anything non-synthetic.
3. Save the input and the asserted canonical output under the dialect's
   directory.
4. Register the fixture in `tools/conformance` so `conformance.yml` replays it.

See [`.claude/rules/normalizers.md`](../../../.claude/rules/normalizers.md) and
the `new-normalizer` skill.
