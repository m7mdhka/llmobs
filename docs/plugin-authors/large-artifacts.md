# Storing large artifacts (blobs) — the interim, and the ruling

**Ruling (Arc N / N4): a first-class `blobs` primitive is NOT part of the frontend arc.**
Adding an eighth-and-a-half data primitive is an ADR-level decision (a new storage seam,
a new adapter surface across both profiles, lifecycle + GC + signed-URL semantics). It is
tracked as its own design arc (issue #120). It is a real, additive gap — but it does not
block the "any language, any framework, plug-and-play" promise, and it deserves its own
small arc, not a cram into N. Until it lands, use the interim below — which is sufficient
for real plugins today.

## The interim — bring your own bucket

Large binary artifacts (a plugin's exported reports, model files, big attachments) do not
belong in `kv` (opaque key→value, size-bounded) or `store` (structured relational
entities, not blobs). Store them in **your own object store**, wired through primitives
that already exist:

1. **Hold the bucket credentials in the `secrets` primitive.** Your plugin backend
   declares a `secrets` capability and stores the S3/GCS/Azure access key (or, better, a
   scoped role/SAS) there — envelope-encrypted, never returned to the frontend.
2. **Call the object store from your backend, under the egress rules.** The upload/
   download runs from your plugin's backend (never the browser), with the mandatory
   [outbound-call rules](trust-model.md#outbound-calls-from-a-plugin-egress-rules-adr-0025-r2):
   a hard timeout, SSRF host-blocking, and the one-convergence-point guard. Hand the
   frontend a short-lived **signed URL** your backend mints, so the browser never sees the
   bucket credential.
3. **Keep only references in kernel storage.** Persist the object key / URL (not the
   bytes) in your `store` collection or `kv`, alongside whatever metadata you query on.
   The canonical model stays the source of truth for telemetry; your blobs are plugin-
   owned artifacts referenced by key.

This keeps the kernel out of the blob path entirely (no new infra dependency for the lite
profile), keeps the credential confined to your backend, and works in both profiles.

## Why not just use `kv`?

`kv` is a small, tenant-scoped key→value store for configuration and modest state — not a
BLOB store. Putting large binaries in it bloats the control-plane row store, has no
streaming/multipart/signed-URL story, and no lifecycle/GC. The bring-your-own-bucket
interim avoids all of that.

## What the future primitive would add

When the `blobs` arc is built it would fold the interim into a first-class primitive: a
kernel-brokered object store (S3/GCS/Azure adapter behind one seam), signed-URL mint,
tenant + plugin scoping, and lifecycle — so a plugin author declares `cap:blobs` instead
of wiring their own bucket. The adapter-abstraction design rules are already banked
(blob object-key filesystem-safety and the S3/GCS/Azure naming/checksum/multipart quirks
— see the open design-rule issues) so the arc starts from a pinned shape.
