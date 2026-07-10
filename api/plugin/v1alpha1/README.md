# Plugin protocol schemas — v1alpha1

The contract between the kernel and a **plugin backend** (Tier-3). Normative prose
is in [`00-overview.md`](00-overview.md); this directory holds the JSON Schemas the
handshake, health, and token payloads validate against.

| Schema | Used by |
|---|---|
| [`handshake.schema.json`](handshake.schema.json) | `GET /plugin/v1/info` response — id, version, protocol, capability echo |
| [`health.schema.json`](health.schema.json) | `GET /plugin/v1/health` response — live/ready + functional watermark |
| [`service-token.schema.json`](service-token.schema.json) | Kernel-issued service-token claims (plugin half of the intersection) |
| [`identity-assertion.schema.json`](identity-assertion.schema.json) | Per-request identity-assertion claims (user half of the intersection) |

Maturity: `v1alpha1`, additive-only within the version (ADR-0011). The Go types +
`Sign`/`Verify` helpers plugins import are in
[`kernel/pkg/pluginproto`](../../../kernel/pkg/pluginproto) and are kept in sync
with these schemas (the schema is the contract; the Go types are DX). See
[ADR-0023](../../../docs/adr/0023-plugin-protocol.md) for the decision record and
the R1–R4 rulings (store physical model, external-URL executor, computed
intersection, store query surface).
