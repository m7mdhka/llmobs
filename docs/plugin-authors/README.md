# Plugin authors

The home of THE tutorial — "your first plugin in Python" — plus the plugin API
reference. A plugin author should be able to ship a custom tab as a container +
manifest without ever reading kernel code.

Covers:

- The seven SDK primitives (query, write, events, jobs, kv, secrets, surface).
- The `llmobs-plugin.yaml` manifest.
- Scaffolding from `templates/` via `llmobs plugin create`.
- The conformance ("verified") bar.
- Distribution: plugin = repo; release = manifest + frontend tarball + OCI image
  digest + sigstore signature + SBOM (D12).

Kept in sync with merged changes by the `docs-writer` agent.
