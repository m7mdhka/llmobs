# Composing with other plugins — the island model (and its escape)

**Ruling (Arc N / N4): the plugin-island model is intentional, and the escape is HTTP.**
A plugin is a self-contained island — it owns its data and its surface, and it does
**not** share an in-process API with another plugin. If plugin A needs something from
plugin B, A integrates with B as a **stranger over HTTP**, exactly as it would with any
third-party service. This is working-as-designed, not a gap.

## Why islands (and why that's the right call)

The whole point of the microkernel is that plugins install, uninstall, and version
independently, in any language, without reading each other's code. A shared in-process
plugin-to-plugin API would undo that: it would couple release cycles, leak internals
across the boundary, and — most importantly — break the security model.

The double-token protocol makes cross-plugin access **inexpressible by construction**
(ADR-0023): every plugin→kernel call carries a token minted for *that* plugin, audience-
bound to `plugin:{its-own-id}`, and the kernel re-intersects each call to the caller's
own `plugin-grant ∩ user ∩ project`. "Obtain another plugin's token" is not a denied
operation — it is one the protocol has no way to name. So a plugin can never reach the
kernel *as* another plugin, and there is no shared runtime handle to reach *into* another
plugin. The island boundary is enforced, not merely conventional.

## The escape — expose your own API, integrate as strangers

When plugins genuinely need to compose, do it the way any two services compose:

1. **Plugin B (the provider) exposes its own HTTP API** from its backend. B is already a
   container behind its own routes (`api/plugins/{B}/*`); add the endpoints you want other
   plugins to call. B owns its authn/authz for those routes — treat callers as untrusted.
2. **Plugin A (the consumer) calls B as an external service.** A stores whatever
   credential B issues in A's own `secrets` primitive, and calls B's URL from A's backend
   under the **egress rules** ([trust-model.md](trust-model.md#outbound-calls-from-a-plugin-egress-rules-adr-0025-r2)):
   a hard timeout, SSRF host-blocking if the URL is config-influenced, and the one-
   convergence-point guard. To A, B is just another URL.
3. **They compose in the shell by co-existing**, not by calling into one another's
   frontends: each plugin owns its nav + surface (the neutral mount contract, ADR-0030);
   the shell renders both. Cross-linking is a normal `<a href>`/route to the other
   plugin's nav path — no shared frontend API.

This keeps every plugin independently installable and versionable, keeps the security
boundary intact (A never acts as B, and never sees B's kernel scope), and needs no new
kernel surface.

## What this is NOT

- **Not an in-process plugin API.** There is deliberately no `sdk.callPlugin("B", …)`.
  That would re-couple plugins and require a cross-plugin token the double-token protocol
  refuses to mint.
- **Not a shared database.** A plugin's `store`/`kv` is tenant- and plugin-scoped; another
  plugin cannot read it. Share data through B's API, not B's storage.

## If the HTTP escape is genuinely insufficient

If you hit a real composition need the HTTP escape can't express — e.g. you want to
declare in a manifest that plugin A is *granted* a specific capability on plugin B, with
the kernel brokering a scoped token — that is a **contract-level** change (a new
manifest grant + a brokered-token mint), not something to hand-roll. Open an issue with
the concrete scenario; it would be its own small design (an ADR), not part of the
frontend arc. As of today no first-party or demand story requires it — the island +
HTTP-escape model covers the real cases.
