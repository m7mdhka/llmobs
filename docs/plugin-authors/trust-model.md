# Plugin trust model — what confines your plugin, and what doesn't

Read this before you decide whether your plugin needs a backend. The honest version:
**backends are confined; frontend-only plugins are trusted-at-install.** Here is
exactly what that means and why.

## The two kinds of plugin identity

| Aspect | Frontend-only plugin | Plugin with a backend |
| --- | --- | --- |
| Where it runs | The shell's browser origin + JS realm (Module Federation, ADR-0004) | Your own HTTP service, out-of-process |
| Its credential | A **frontend token** (J1): kernel-minted, `plugin-grant ∩ session ∩ project` | The **double token** (H3): service token + per-request identity assertion |
| Confined? | **Least-privilege by default, NOT a hard boundary** | **Yes — genuinely confined** |
| Trust posture | **Trusted-at-install** (like a browser/IDE extension) | Untrusted code can be safely run |

## Frontend tokens — least-privilege by default

When your frontend uses the SDK data hooks (`useQuery`, `useWriteScore`, …), the
shell hands the SDK a **frontend token** minted for your plugin. Its scopes are your
manifest grant **intersected with** the signed-in user's session **and** the active
project, computed by the kernel. So, with no work on your part:

- a plugin that only declares `traces:read.metadata` **cannot** read payloads;
- your plugin **cannot** exceed what the current user is allowed to see;
- your plugin **cannot** reach another project's data.

This is the right default and you get it for free. Use the SDK; don't hand-roll
`fetch`.

## The honest limitation — it is not a boundary

Your frontend runs in the **same browser origin and JavaScript realm as the shell**
(that is how Module Federation composes it, with a shared React/SDK). Nothing in the
browser stops plugin code from ignoring the SDK and calling the Query API with the
**ambient session cookie** — which would run at the user's *full* session scope, not
your intersected grant.

We do not hide this. It is demonstrated by a passing test
(`query.TestFrontendTokenIsNotABoundary`) and stated here plainly: **the frontend
token confines cooperating code; it does not contain hostile code.** A frontend-only
plugin is therefore **trusted-at-install** — installing one is trusting it with the
installing user's session, the same way you trust a browser extension. (An install
flow does not exist yet; when it is built, it MUST state this consent explicitly —
tracked alongside origin isolation.)

## Need hard confinement? Ship a backend

If your plugin must run code that should *not* be trusted with the user's full
session — third-party, untrusted, or security-sensitive — put the logic in a
**backend**. The kernel's double token genuinely confines backends: every data call
is re-intersected server-side (`plugin-grant ∩ user ∩ project`), the session cookie
is never forwarded, and audience-bound assertions prevent one plugin from acting as
another. See [`api/plugin/v1alpha1/00-overview.md`](../../api/plugin/v1alpha1/00-overview.md).

## The future: origin isolation

A real boundary for *untrusted frontend* plugins — a sandboxed iframe on a distinct
origin with a postMessage data bridge, where the frontend token is the **only**
credential and there is no ambient cookie to fall back to — is specified as a
follow-up (ADR-0004 amendment, tracked in ADR-0023's deferred list). It will be built
when running an untrusted third-party frontend is a real requirement (a marketplace,
or a specific customer), not before. Until then, the rule above holds: **untrusted
logic goes in a backend.**

## Outbound calls from a plugin (egress rules, ADR-0025 R2)

If your plugin backend calls an external service — an LLM provider for an eval, a
webhook, anything at a URL — these rules are **non-optional**. They protect the
platform, not just your plugin, because in the **lite profile every plugin shares one
host process** (`runtimes/shared/`): one hung outbound call can starve *every* plugin
in that process.

- **Always set a hard timeout on the call and propagate the caller's cancellation.**
  Never issue an un-timed-out request. An SDK's default timeout is not enough — some
  providers/custom base-URLs ignore it; enforce your own ceiling around the fetch.
- **Enumerate, don't allowlist.** If you support several providers, derive the set
  from an enum, not a hand-maintained list — allowlists drift and leave one provider's
  egress unbounded (this is exactly how Langfuse left three providers un-timed-out).
- **Block internal / link-local hosts (SSRF).** If any part of the destination is
  user- or config-influenced, refuse `127.0.0.0/8`, `10/8`, `169.254.0.0/16`,
  `metadata.google.internal`, and friends — an eval/webhook that can be pointed at an
  internal address is an SSRF primitive.
- **Guard at the one convergence point**, not per-entry-point. Put the timeout +
  host-check where every outbound path funnels through, so a new call site inherits
  them by construction (per-entry guards drift; a new path forgets one).

The kernel side of this is watchdogged: a plugin that pins the shared runtime with a
hung call trips the two-signal health machinery (its functional watermark stalls →
`degraded`) and the supervisor can restart it. But a restart is a blunt recovery —
your plugin's own timeout is the first and best line. When the kernel grows its own
outbound surface (evals, webhooks), it will carry these same rules built in (ADR-0025).
