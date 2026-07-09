# deploy/operator — Kubernetes operator + CRD

The Kubernetes operator that acts as the supervisor's **operator executor**
(D6). It reconciles the desired plugin state the kernel owns into running
workloads via a custom resource, `LLMObsPlugin` (the CRD lives here).

The kernel owns only desired state + health; it never talks to the Kubernetes
(or Docker) API directly. The operator watches the CRD and does the reconciling.
