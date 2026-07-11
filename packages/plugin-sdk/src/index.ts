// @llmobs/plugin-sdk v0 — the FRAMEWORK-NEUTRAL Tier-2 plugin surface (Arc N / N1,
// ADR-0030). This entry imports NO React: a plugin frontend written in any framework
// (React, Vue, Svelte, vanilla) exports the neutral `mount(element, context)` contract
// and consumes these plain primitives. The React binding — hooks + provider + the
// design system — lives at `@llmobs/plugin-sdk/react`, a thin adapter over this core.
// See CHANGELOG.md.

// The framework-neutral mount contract (what a plugin exports; what the shell passes in).
export type {
  PluginMount,
  PluginUnmount,
  PluginModule,
  PluginMountContext,
  PluginUser,
  PluginTheme,
  TextDirection,
} from "./mount.js";

// Data primitives: `query` + `write` (score writes). DataClient is a plain fetch class —
// the G1 frontend-token enforcement (fail-closed) is here, framework-independent.
export { DataClient, SdkError } from "./client.js";
export type { QueryResponse, TraceTree, ClientConfig, QueryInput, ScoreInput, SettingsView, FrontendTokenProvider, LLMObsClient } from "./client.js";

// Data primitive: `kv` (per-plugin, tenant-scoped key/value; backend double-token).
export { KvClient } from "./kv.js";
export type { KvConfig } from "./kv.js";

// Data primitive: `secrets` (per-plugin encrypted store; backend double-token).
export { SecretsClient } from "./secrets.js";
export type { SecretsConfig, SecretInfo } from "./secrets.js";

// Data primitive: `store` (plugin-owned structured collections; backend double-token).
export { StoreClient } from "./store.js";
export type { StoreConfig, StoreQuery, StoreFilter, StoreOp, StorePage } from "./store.js";

// Data primitive: `events` (durable subscribe; at-least-once; backend double-token).
export { EventsClient } from "./events.js";
export type { EventsConfig, Event } from "./events.js";

// Capability: `ingest` (compat plugins push OTLP through the kernel pipeline).
export { IngestClient } from "./ingest.js";
export type { IngestConfig } from "./ingest.js";

// Manifest types (mirror of the JSON Schema contract).
export type {
  PluginManifest,
  PluginFrontend,
  PluginNavItem,
  Capability,
} from "./manifest.js";

// Canonical entity + query types, straight from the generated client.
export type {
  LLMObsCanonicalSpanV1Alpha1 as Span,
  LLMObsCanonicalTraceV1Alpha1 as Trace,
  LLMObsQueryDSLDocumentV1Alpha1 as QueryDocument,
} from "@llmobs/query-client";

// String-externalization seam (N3) — the mechanism to localize plugin strings.
export { createTranslator } from "./i18n.js";
export type { MessageCatalog, Translator } from "./i18n.js";

// Theme tokens + locale/direction helpers — CSS custom properties + document-root
// application, framework-neutral (any binding uses them). Includes directionForLocale /
// getLocale / applyLocale (N3). The React design-system COMPONENTS (@llmobs/ui) are
// re-exported from `@llmobs/plugin-sdk/react`, not here.
export * from "@llmobs/tokens";
