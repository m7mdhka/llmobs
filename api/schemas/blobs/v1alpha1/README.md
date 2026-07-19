# Blobs primitive — v1alpha1

Kernel-brokered large-artifact storage: the `blobs` data primitive (ADR-0037). Plugins
store large binary artifacts (exported reports, model files, attachments) without a
bring-your-own-bucket setup — `kv` is for small values, `store` for structured entities,
`blobs` for bytes.

Follows the same K8s-style maturity rules as the rest of `api/`: additive-only within a
maturity version.

## Capability and scope

Gated on the `blobs` capability (manifest `capabilities: [blobs]` → `cap:blobs`). Every
object is scoped to the caller's server-derived `(plugin_id, project_id)`: a plugin
addresses a blob by a **logical key** it chooses, and the kernel derives a tenant-scoped
physical key at one seam, so a plugin can only ever read/write/delete its own objects. A
logical key that is empty, over 80 bytes, or contains a NUL is rejected `400`.

## Operations (double-token, `POST`)

All under `/v1alpha1/plugin/blobs`, authorized by the plugin service token + identity
assertion; the logical key is the `key` query parameter.

| Op | Request | Response |
| --- | --- | --- |
| `put?key=<k>` | body = the bytes; `Content-Type` header preserved | `204`; `413` if the body exceeds the configured ceiling (default 64 MiB) |
| `get?key=<k>` | — | `200` with the bytes + stored `Content-Type` (+ `Content-Length`); `404` if absent |
| `delete?key=<k>` | — | `204` (idempotent — deleting a missing key is not an error) |

## Two profiles, one behavior

The backend is chosen by config: the local filesystem adapter (lite) or the
S3-compatible adapter (scale, `BLOB_S3_ENDPOINT`), behind the one `blob.Store` seam. Both
are held to the same cross-adapter conformance, so the observable behavior is identical.

**Scale deployment (operator) requirements:** the bucket must have block-public-access and
a default-encryption policy — encryption-at-rest rests on the bucket policy (the portable
choice across S3/MinIO/GCS). The kernel warns loudly at startup if the bucket has no
default-encryption policy.

## Not yet in v1alpha1 (tracked follow-ons)

Signed URLs (offload the transfer from the kernel), lifecycle/GC (orphan reaping,
per-tenant quota). Until then, get/put stream through the kernel (works on both profiles).
