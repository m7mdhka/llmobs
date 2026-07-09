# deploy/

Deployment assets for both profiles (D6, D7). The kernel never touches the
Docker socket; orchestration goes through supervisor executors.

| Path | Purpose |
|---|---|
| `compose/` | **Lite** profile: a base compose file plus generated per-plugin overlays. `docker compose up`, Postgres-only, shared plugin runtime. |
| `helm/llmobs/` | **Scale** profile: an umbrella Helm chart — kernel, toggleable dependencies (ClickHouse, queue, S3), and plugins. |
| `operator/` | The Kubernetes operator and the `LLMObsPlugin` CRD (supervisor's operator executor). |
| `airgap/` | Offline bundle build + install scripts (D14). No external URLs at install time. |

See [`.claude/rules/deploy.md`](../.claude/rules/deploy.md).
