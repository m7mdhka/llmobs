// The framework-neutral frontend mount contract (Arc N / N1, ADR-0030). A plugin
// frontend's Module-Federation-exposed module exports a `mount(element, context)`
// function and returns an `unmount()` cleanup — it may render with React, Vue, Svelte,
// or vanilla DOM. React is ONE binding over this contract (@llmobs/plugin-sdk/react),
// not THE binding. Nothing here imports React: this is the neutral core.
//
// The shell builds `context` (the token-confined data client, the active project, the
// user, theme, locale) and calls `mount`; the plugin renders into `element` and returns
// a function the shell calls to tear the surface down on route change. The G1 frontend
// token is enforced INSIDE the data client (client.ts `headers()` fails closed), so a
// non-React caller reaching `context.client` is confined to plugin-grant ∩ session ∩
// project identically to a React one — the framework is irrelevant to the boundary.

import type { LLMObsClient } from "./client.js";

/** The signed-in user (display/context only — NEVER trusted for authorization; the
 *  frontend token carries the authoritative, intersected scopes). */
export interface PluginUser {
  id: string;
  email: string;
  role: string;
}

/** Text direction. Threaded through the contract now (N1) so locale support (N3) does
 *  not have to reopen it — a plugin reads `direction` and lays out LTR/RTL. */
export type TextDirection = "ltr" | "rtl";

/** Theme handoff. The shell owns the CSS custom properties (from @llmobs/tokens) on the
 *  document root, so a plugin styles against those variables regardless of framework and
 *  gets live light/dark theming for free. `mode` is exposed for the rare case a plugin
 *  must branch in JS — but note it is captured at MOUNT and is NOT reactive today (a
 *  theme toggle updates the CSS variables live, not this value; re-read on remount). */
export interface PluginTheme {
  mode: "light" | "dark";
}

/**
 * PluginMountContext is what the shell passes to `mount`. It is a plain object — no
 * React, no hooks — so any framework can consume it. Every field the React
 * provider/hooks expose is available here directly.
 */
export interface PluginMountContext {
  /** Gateway base URL; "" means the shell's own origin. */
  baseUrl: string;
  /**
   * The URL path prefix the shell mounted this surface at (e.g. "/traces"). A plugin
   * that does its own routing uses this as its router base, so its links/navigation stay
   * under the shell's URL — a React plugin passes it to `<BrowserRouter basename>`, a Vue
   * plugin to `createRouter({ history: createWebHistory(basePath) })`. Required because a
   * neutral surface renders in its OWN root and cannot inherit the shell's router context.
   */
  basePath: string;
  /** The active project, or undefined if none is selected. */
  project?: { id: string };
  /** The signed-in user (display only). */
  user?: PluginUser;
  /**
   * The token-confined data client (`query`/`write`/settings). Built by the shell under
   * the plugin's frontend token, so calling it runs at least-privilege and FAILS CLOSED
   * if no token can be obtained — the G1 enforcement is in the client, not the binding.
   *
   * NOTE — the raw frontend-token provider is DELIBERATELY not on this context: the shell
   * owns it and closes over it inside this client, so untrusted plugin code cannot read
   * the bearer token off the context and exfiltrate it (it would be a portable, off-origin
   * credential). A future primitive that needs its own confined client (kv/events
   * frontend) will get a shell-owned client FACTORY, never the raw minting getter.
   */
  client: LLMObsClient;
  /** Current theme. */
  theme: PluginTheme;
  /**
   * BCP-47 locale (e.g. "en", "ar-EG"). N1 threads the field; N3 wires real detection
   * and the string-externalization seam. Defaults to "en" until then.
   */
  locale: string;
  /** Text direction for the current locale. Defaults to "ltr". */
  direction: TextDirection;
}

/** The cleanup a plugin returns from `mount`; the shell calls it to tear the surface
 *  down (route change, plugin disable). It MUST be idempotent and remove all of the
 *  plugin's DOM, listeners, and timers from `element`. */
export type PluginUnmount = () => void;

/**
 * The framework-neutral mount function a plugin's exposed module MUST export as `mount`.
 * `element` is an empty container the plugin owns until `unmount`. Returning `void` is
 * allowed for a plugin that needs no teardown, but returning an `unmount` is strongly
 * preferred (leaks otherwise accrue across navigations).
 */
export type PluginMount = (element: HTMLElement, context: PluginMountContext) => PluginUnmount | void;

/** The shape the shell resolves from a plugin's MF-exposed module. `mount` is the
 *  contract; a legacy React-default-component export is no longer supported (ADR-0030
 *  supersedes the ADR-0004 React-component contract). */
export interface PluginModule {
  mount: PluginMount;
}
