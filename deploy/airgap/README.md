# deploy/airgap — offline bundle

Build and install scripts for **air-gapped** deployments (D14) — a first-class,
release-blocking CI target (the `airgap` workflow).

- The build step assembles an offline bundle: all container images, Helm charts,
  and static assets pinned by digest.
- The install step runs in a **network-isolated** context — **no external URLs**
  at install time. Anything the install needs must be inside the bundle.

If a change here reaches out to the network at install time, it fails the air-gap
gate.
