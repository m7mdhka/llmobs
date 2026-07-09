---
name: release
description: Cut a platform version-train release following gitflow — create release/vX.Y.0, stabilize, cz bump, merge to main and back-merge to develop, and verify tags/artifacts. Maintainers only.
---

# Release (gitflow version train)

The platform (kernel + shell + first-party plugins + charts) releases as one
version train tagged on `main`. This is a maintainer-only procedure.

## Preconditions
- You are a maintainer.
- `develop` is green (CI passing) and contains everything intended for release.
- Working tree clean; you are up to date with the remote.

## Steps

1. **Cut the release branch from `develop`:**
   ```sh
   git checkout develop && git pull
   git checkout -b release/vX.Y.0
   ```
2. **Stabilize.** Only bug fixes and release chores land on `release/*` — no new
   features. Each fix is a signed-off conventional commit.
3. **Run the release-blocking gates locally / in CI:**
   `make lint test conformance e2e e2e-k8s airgap perf`. These must pass; the
   air-gap and Helm/kind e2e jobs are release-blocking, not optional.
4. **Bump the version, changelog, and tag:**
   ```sh
   cz bump   # computes vX.Y.0 from Conventional Commits, writes CHANGELOG, tags
   ```
   If the SDK or a schema also releases, tag it independently
   (`plugin-sdk/vA.B.C`) per VERSIONING.md.
5. **Merge to `main` (merge commit, no squash — preserve history):**
   ```sh
   git checkout main && git merge --no-ff release/vX.Y.0
   git push --follow-tags
   ```
6. **Back-merge to `develop`** so the bump/changelog/fixes are not lost:
   ```sh
   git checkout develop && git merge --no-ff release/vX.Y.0 && git push
   ```
7. **Delete the release branch** and verify:
   - The `vX.Y.0` tag exists on `main` and pushed.
   - `release.yml` produced multi-arch images, published the Helm chart, and
     attached SBOM + sigstore signatures to the GitHub Release.
   - The compatibility matrix in `docs/` reflects the new train ↔ plugin API
     mapping.
   - npm packages published for any tagged `packages/*`.

## Never
- Tag anywhere but `main`.
- Squash `release/*` or `hotfix/*` into `main`.
- Skip the air-gap or kind e2e gates.
