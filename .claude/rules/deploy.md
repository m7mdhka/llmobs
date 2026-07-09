---
scope: ["deploy/**"]
---

# Deploy

Every feature must work in BOTH profiles: lite (Postgres-only, compose, shared
plugin runtime) and scale (ClickHouse, Helm, isolated containers). The kernel
never touches the Docker socket — orchestration goes through supervisor
executors.

- **Every Helm chart value is documented.** No undocumented knobs in
  `values.yaml`; each value has a comment explaining purpose and default.
- **kind e2e must pass.** Changes under `deploy/helm` or `deploy/operator` are
  validated by the Kubernetes e2e (kind + Helm install + operator reconcile +
  plugin lifecycle). Treat it as release-blocking.
- **Airgap = no external URLs.** The offline bundle installs in a
  network-isolated context. Nothing under `deploy/airgap` may fetch from the
  network at install time; all images/charts/assets are bundled.
- **Two profiles, one Query API.** Don't let a chart or overlay introduce
  behavior that only exists in one profile.
- **Executors, not the socket.** Supervisor reconciliation happens through the
  cli-compose, operator, gitops, or external-url executors — never by the kernel
  calling Docker/K8s directly.
