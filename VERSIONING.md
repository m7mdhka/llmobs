# Versioning Policy

LLMObs reconciles a monorepo with independently-consumed contracts by
versioning at two levels: the **platform version train** and the
**independently-versioned contracts** (the plugin SDK and the plugin / manifest
/ event API schemas). This document is the formal policy (design decision D11 /
D14).

## 1. The platform version train

The platform — kernel + web shell + first-party plugins + Helm charts — releases
as **one version train**: `v0.x.y`, tagged on `main` per gitflow. This is the
single number users think about ("I'm running LLMObs v0.7.2").

- Managed by `cz bump` at the repo root: it computes the next version from
  Conventional Commits, updates [`CHANGELOG.md`](CHANGELOG.md), and creates an
  annotated `v$version` tag.
- Tags are only created on `main`, and every merge to `main` is a train release.
- While the platform is pre-1.0 (`v0.x.y`), minor versions may contain breaking
  changes to internal behavior, but published contracts still follow the
  maturity rules below.

## 2. Independently-versioned contracts

Plugin authors depend on these across train releases, so they carry their own
versions:

### `@llmobs/plugin-sdk` — semantic versioning
- Semver-sacred. A breaking change is a **major** bump.
- Released from `main` with its own tags: `plugin-sdk/vX.Y.Z`.
- Published to npm by the release workflow when a matching tag is pushed.

### Plugin API / manifest schema / event schema — Kubernetes-style maturity
Each evolves through maturity levels:

```
v1alpha1  →  v1beta1  →  v1
```

- **Additive-only within a major.** You may add optional fields; you may not
  remove or repurpose existing ones.
- **Breaking a published contract requires an ADR and a deprecation window.**
- Old maturity directories are retained (e.g. `api/schemas/manifest/v1alpha1/`
  stays even after `v1beta1/` exists) until formally removed at the end of a
  deprecation window.

## 3. Compatibility matrix

The relationship between a platform train release and the plugin API versions it
supports is published as a **generated table in [`docs/`](docs/)** and tested in
CI. Plugin authors consult this matrix to know which platform versions accept
their manifest and SDK versions.

## 4. Deprecation windows

When a contract element is deprecated:

1. Mark it deprecated in the contract and note it in the changelog and docs.
2. Keep it functional for at least the deprecation window (defined per contract,
   minimum one minor train release, extended for `v1`/GA contracts).
3. Remove it only in a release that documents the removal, with an ADR.

## 5. Air-gapped installs

Air-gapped installation is a first-class, release-blocking CI target. Every
train release must produce an offline bundle that installs without external
network access (see [`deploy/airgap/`](deploy/airgap/)).
