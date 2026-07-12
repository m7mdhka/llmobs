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

## Checklists harvested from the Opik cross-over mine (verify our charts/bundle)

Second-incumbent (Opik/Comet) self-hosting pain — each is a thing to confirm our
deploy assets already handle. Every item cites a public `comet-ml/opik` issue,
checkable at `https://github.com/comet-ml/opik/issues/<n>`:

- **Airgap must survive a vanished OR relicensed upstream image.** Opik #3172/#3305: a
  self-host deploy broke because a referenced upstream image (a MinIO/bitnami tag) no
  longer existed in the registry. Opik #2764 is the supply-side twin: the Bitnami
  catalogue was deprecated/relicensed out from under dependents. Confirm `deploy/airgap`
  bundles every image **by digest** and never resolves a floating upstream tag at install
  time (ties the "no external URLs" rule above) — the bundle must install even if the
  upstream image was deleted, retagged, or relicensed. Digest-pinning is what makes the
  bundle immune to *both* failure modes; a tag reference is immune to neither.
- **Helm-values coverage checklist.** Opik repeatedly patched flexibility gaps —
  sub-path ingress (#3291, `example.com/tools/opik`), custom service labels (#3783),
  duplicate labels (#3089), configurable ExternalSecrets `ClusterSecretStore` name
  (#4033), and **Ingress TLS `secretName` (#2366 — their Ingress template had no way to
  reference an existing TLS secret at all)**. Verify our chart exposes (and documents,
  per the rule above): ingress path prefix / sub-path, **ingress TLS `secretName`**,
  custom pod+service labels with no duplicates, and configurable external-secret-store
  references.
- **Image hygiene (low priority).** Opik #7107 dropped an unused `perl` interpreter to
  clear a CVE. Audit `deploy/compose/kernel.Dockerfile` (and any runtime image) for
  unused interpreters/toolchains that only enlarge the CVE surface.
