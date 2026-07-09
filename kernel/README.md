# kernel/

The Go microkernel. Module: `github.com/m7mdhka/llmobs/kernel`.

The kernel owns **only**: OTLP ingestion + dialect normalizers, storage
adapters, auth/tenancy, plugin registry + supervisor, event bus, the typed Query
API, and jobs/kv/secrets. Every product feature lives in a plugin, built on the
public plugin API (the dogfood rule, D2).

## Two import boundaries (compiler-enforced)

- **`internal/`** — private by construction. Go's `internal/` semantics mean
  nothing here is importable from `cli/`, `web/`, or plugins. This is where the
  kernel's implementation lives.
- **`pkg/`** — the public, plugin-facing surface. Plugins may import
  `pkg/pluginproto`, `pkg/model` (and nothing else from the kernel).

## Layout

```
cmd/llmobsd/      kernel daemon entry point
internal/
  gateway/        routing, auth termination, identity assertions, circuit
                  breakers, rate limits, frontend bundle serving
  controlplane/
    registry/     plugin manifests, versions, permissions
    supervisor/   lifecycle state machine, health, watermarks
    executors/    cli-compose | operator | gitops | external-url (D6)
    auth/         sessions, OIDC, API keys, token minting, RBAC
    audit/        security-relevant audit log
  dataplane/
    ingest/       OTLP receivers (gRPC 4317 / HTTP 4318)
    pipeline/     middleware chain (D10): authenticate -> decode -> normalize
                  -> redact -> sample -> enrich -> persist -> publish
    normalize/    dialect normalizers, one pure file per dialect (D3)
    query/        typed DSL parser + planner (D9), compiled per adapter
    eventbus/     durable consumer groups, dead-letter
  storage/        adapters behind interfaces: postgres/, clickhouse/, blob/
  platform/       config, logging, jobs scheduler, kv, secrets, telemetry
pkg/
  model/          canonical span/trace/score types (generated + hand-written)
  pluginproto/    plugin protocol types + token-verification helpers
  brand/          D15: the ONE place the product name lives (Go)
migrations/       kernel DB migrations (postgres/, clickhouse/)
testdata/
  fixtures/       SDK conformance fixtures: recorded (synthetic) traffic per
                  dialect, replayed in CI (D3)
```

No source files are prescribed here — they are added per task. See
[`.claude/rules/go-style.md`](../.claude/rules/go-style.md) and
[`.claude/rules/normalizers.md`](../.claude/rules/normalizers.md).
