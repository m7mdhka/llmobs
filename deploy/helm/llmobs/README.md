# deploy/helm/llmobs — scale profile umbrella chart

The umbrella Helm chart for the scale profile: kernel, toggleable dependencies
(ClickHouse, queue/NATS, S3-compatible blob), and plugins as isolated
containers. Same Query API above the adapter line as lite.

- **Every `values.yaml` value is documented** — no undocumented knobs.
- Validated by the `e2e-k8s` job (kind + Helm install + operator reconcile +
  plugin lifecycle), which is release-blocking on `release/*`.

See [`.claude/rules/deploy.md`](../../../.claude/rules/deploy.md).
