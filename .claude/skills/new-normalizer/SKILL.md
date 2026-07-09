---
name: new-normalizer
description: Add an ingestion dialect normalizer using the fixture-first workflow — record a fixture, write the pure mapping, assert canonical output, wire it into conformance. Use when adding support for a new SDK/wire dialect on the ingestion hot path.
---

# New normalizer (fixture-first)

Normalizers map a wire dialect onto the canonical model in the ingestion hot
path. They are pure functions, one file per dialect, and every mapping change is
proven by a fixture. (First decide: hot-path normalizer vs. cold-path compat
plugin. This skill covers the normalizer.)

## Steps

1. **Record a synthetic fixture first.** Capture representative spans for the
   dialect and save them under
   `kernel/testdata/fixtures/<dialect>/`. Fixtures are **synthetic only** — no
   real user prompt/completion payloads. Follow the recording procedure in that
   directory's README.
2. **Write the normalizer** as one file in
   `kernel/internal/dataplane/normalize/<dialect>.go`:
   - Pure function: attributes in → canonical fields out. No I/O, no clock, no
     global state.
   - **Preserve raw attributes** alongside the canonical fields — never drop or
     overwrite them.
3. **Assert canonical output.** Add a table-driven test that replays the fixture
   through the normalizer and asserts the canonical `pkg/model` result.
4. **Wire into conformance.** Add the dialect to `tools/conformance` so
   `conformance.yml` replays it in CI (D3).
5. **Register the dialect** in the pipeline's normalize step so ingestion selects
   it by content-type / detection.
6. **Docs.** Note the newly supported dialect in `docs/`.

## Done when
- Fixture committed, normalizer pure and raw-preserving, test asserts canonical
  output, conformance covers the dialect, `make conformance` passes.
