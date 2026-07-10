# Template: plugin-python

Scaffold for a Python plugin backend. Born-identical to the working
`plugins/langfuse-compat` backend — the same `llmobs_plugin.py` protocol library,
the same packaging — so what you learn here transfers directly (and it's the exact
starting point the n8n flagship and every Python author begins from).

## Layout
- `backend/llmobs_plugin.py` — the public protocol library: verify kernel-signed
  identity assertions (Ed25519), hold a service token, call kernel primitives
  (ingest / kv / secrets / store). Do not edit — it IS the contract client.
- `backend/app.py` — your FastAPI service. Implements the handshake
  (`/plugin/v1/info`) + two-signal health (`/plugin/v1/health`); add your own
  endpoints and logic.
- `backend/{requirements.txt,Dockerfile}` — run it however you like; the kernel
  reaches it by URL (external-URL executor).
- `llmobs-plugin.yaml` — the manifest: declare `capabilities`, `permissions`, and
  `spec.backend`. The kernel grants only what the manifest requests.

## Contract notes (learned the hard way — see docs/plugin-authors/language-agnostic-findings.md)
- Token segments are **unpadded** base64url — `llmobs_plugin` re-pads for you.
- Fetch the kernel public key from `GET /v1alpha1/plugin/kernel-key` to verify
  assertions (`KernelClient.fetch_kernel_pubkey`).
- Cold-path `ingest` is **service-token-only** (no user assertion).

## Local check
`cd backend && python3 interop_test.py` (needs `cryptography`) verifies a
kernel-signed token vector — the language-agnostic proof, runnable offline.
