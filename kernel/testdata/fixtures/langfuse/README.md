# Fixtures: Langfuse

Langfuse wire-format traffic. Note the architectural distinction (D3): Langfuse
compatibility is a **compat plugin** with `ingest:write` capability (cold path,
its own endpoint), not a hot-path normalizer. These fixtures exercise that compat
path end-to-end and assert canonical output.

Synthetic data only. See the parent [README](../README.md) for the recording
procedure.
