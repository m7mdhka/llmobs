# ADR-0037: A first-class `blobs` primitive (large-artifact storage)

- **Status:** Accepted — design pinned; foundation (seam + key derivation + local
  adapter) landed; gateway/SDK/scale-adapter/GC are the tracked build follow-on.
- **Date:** 2026-07-19
- **Deciders:** m7mdhka
- **Relates to:** ADR-0023 (plugin protocol — the primitive set this extends), the
  `bus.Store` / `storage.Store` seam pattern this mirrors, ADR-0025 R2 (outbound-call /
  egress rules — relevant to signed-URL fetches). Supersedes the interim in
  `docs/plugin-authors/large-artifacts.md` once the build lands. Evidence: the
  plugin-viability audit's flexibility cluster (issue #120) and the banked blob design
  rules #101 (adapter abstraction) and #92 (filesystem-safe keys).

## Context

The seven data primitives have no home for large binary artifacts — a plugin's exported
reports, model files, big attachments. `kv` is opaque key→value, size-bounded, with no
streaming / signed-URL / GC; `store` is structured relational entities. The interim
answer (bring-your-own-bucket: creds in `secrets`, upload from the plugin backend under
the egress rules, references in `store`) works today but keeps the kernel out of the blob
path and pushes bucket lifecycle onto every plugin author.

A first-class `blobs` primitive gives plugins kernel-brokered large-artifact storage: a
new storage seam and an object-store adapter across BOTH profiles, tenant + plugin
scoping, signed-URL access, and lifecycle/GC. It is additive — it does not block the
"any language, any framework, plug-and-play" promise — and it is ADR-level, so its design
is pinned here before the surface is built.

## Decision

Add a `blobs` data primitive, structured exactly like the other kernel seams: a small,
backend-agnostic `blob.Store` interface with the kernel owning tenancy and key safety,
and a backend implementing only persistence.

### The seam and the ONE key-derivation convergence point

`blob.Store` is deliberately **dumb about tenancy** — `Put`/`Get`/`Delete` take a physical
key and never see a project or plugin id. Every caller MUST turn a plugin-supplied logical
key into a physical key through `blob.DeriveKey(projectID, pluginID, logicalKey)`, which
folds in the **server-derived** (project, plugin) from the assertion. Because that is the
only way to mint a physical key, a plugin cannot address another tenant's or another
plugin's objects **by construction**, enforced at this one seam rather than re-checked per
backend (the convergence-point invariant).

`DeriveKey` produces `b/<enc(project)>/<enc(plugin)>/<enc(logicalKey)>` where `enc`
percent-encodes every byte outside `[A-Za-z0-9_-]` (including `.` and `/`). This makes the
key simultaneously:

- **injective** — distinct (project, plugin, key) triples never collide, and an encoded
  `/` (`%2F`) inside any component cannot forge a segment boundary, so there is no
  cross-tenant aliasing (#120 tenant isolation);
- **filesystem-safe (#92)** — every segment is bounded well under `NAME_MAX` (the raw
  logical key is capped so its 3×-worst-case encoding stays under 255 bytes), contains only
  `[A-Za-z0-9_-]` plus `%HH`, and can never be `.`/`..` or contain a NUL — no traversal, no
  reserved-name hazard;
- **object-store-safe (#101)** — the same encoded key is a valid S3/GCS/Azure object key,
  so ONE derivation serves every adapter (the `/` delimiters map to key prefixes) and no
  per-adapter key logic can drift.

### Two profiles, one interface

- **Lite / dev / airgap:** a local filesystem `Store` (landed) — objects as files under a
  root, atomic write-via-rename, a content-type sidecar, and a defense-in-depth
  root-containment guard so even a raw traversal key that bypassed `DeriveKey` cannot
  escape the root.
- **Scale:** an S3-compatible `Store` (landed) — same interface, same derived keys, built
  on `minio-go` (Apache-2.0), the de-facto minimal S3-compatible client. It targets AWS
  S3, MinIO, and GCS/Azure via their S3 gateways, which is exactly the #101 "abstract the
  backend naming/checksum/multipart quirks" requirement — met by staying on the S3 API
  surface rather than a per-cloud SDK. This is the codebase's first S3 client (the WAL
  archival tier's S3 sink was deferred); the same dependency will serve it when built.

  **Security requirement (operator):** objects are written private (the S3 default — never
  a public-read ACL). Encryption-at-rest is enforced by the **bucket's default-encryption
  policy** (operator-configured, SSE-S3 or SSE-KMS), which applies to every write
  automatically. This is the only portable choice — forcing a per-object SSE header couples
  the adapter to the backend's KMS setup, and a stock MinIO without KMS rejects it (a real
  bug the MinIO conformance run caught). The adapter exposes an opt-in per-object SSE knob
  for KMS-configured backends. Operators MUST enable bucket default-encryption and
  block-public-access; this is a documented deployment requirement.

### What this ADR lands now vs. defers

**Landed (foundation):** the `blob.Store` seam, `DeriveKey` (the security core) with
injectivity / fs-safety / cross-tenant prove-the-negative tests, and the local filesystem
adapter with its containment proof.

**Landed (scale adapter):** the S3-compatible `Store` on `minio-go`, held to the same
cross-adapter conformance as the local adapter (local runs always; S3 runs against a real
MinIO when `LLMOBS_BLOB_S3_TEST_ENDPOINT` is set — the run that caught the SSE/KMS bug).

**Tracked build follow-on** (authorized by this ADR, gated on review as a plugin-facing
change): the `cap:blobs` manifest capability; the gateway `blobs` endpoints (put / get /
delete, gated on `cap:blobs`, tenant+plugin scoped at the seam) with a cross-tenant
prove-the-negative; the `@llmobs/plugin-sdk` `BlobsClient`; the per-profile wiring (local
for lite, S3 for scale) — the change that first EXPOSES `cap:blobs`, and which MUST ship
both adapters together so the primitive works in both profiles (invariant 9). Then, as
enhancements: signed-URL mint (kernel-brokered for lite, presigned for S3, under the
ADR-0025 R2 egress rules); lifecycle/GC (orphan reaping, **per-tenant storage + object-count quota** — blobs raises
the per-object ceiling to 64 MiB, so it is the primitive that most needs an aggregate cap;
tracked with the GC work); and a
**fail-loud encryption-at-rest check** — at scale-profile startup, query the bucket's
default-encryption policy (`GetBucketEncryption`) and log a prominent warning (or refuse to
start) if it is absent, so a mis-configured bucket that would write blobs unencrypted is
caught at boot rather than trusted to documentation (the security review's one defense-in-
depth note). Until the surface lands, the bring-your-own-bucket interim remains documented
and sufficient.

## Consequences

- The riskiest part — tenant-isolating, traversal-proof key derivation — is settled and
  tested up front, so the gateway/SDK build is low-risk wiring over a proven seam.
- Adding `blobs` is additive to the primitive set; nothing existing changes.
- The local adapter gives the lite profile a real blob backend with no new infra
  dependency (files under a root), preserving the single-dependency lite promise.

## Alternatives considered

- **Keep bring-your-own-bucket only.** Rejected as the end state: it pushes bucket
  lifecycle and credential handling onto every plugin author and keeps large-artifact
  UX inconsistent across plugins. Retained as the interim until the build lands.
- **Overload `kv` for blobs.** Rejected: `kv` is a small tenant-scoped key→value store;
  large binaries bloat the control-plane row store and it has no streaming/signed-URL/GC.
- **Per-adapter key derivation.** Rejected: it would let the tenant-isolation and
  fs-safety guarantees drift between the local and S3 backends. One shared `DeriveKey` is
  the convergence point.
