// @llmobs/plugin-sdk v0 — the Tier-2 plugin surface. A plugin frontend imports
// ONLY this package (which re-exports the design system): the provider, data
// hooks, manifest types, and the standard degraded-state components. Semver
// starts at 0.1.0; see CHANGELOG.md.

// Provider + context (the shell supplies it; plugins consume via hooks).
export { LLMObsPluginProvider, useLLMObs } from "./context.js";
export type { LLMObsContextValue, ProviderConfig } from "./context.js";

// Data primitives: `query` + `write` (score writes).
export { useQuery, useTraces, useTrace, useWriteScore } from "./hooks.js";
export type { AsyncState, TracesParams, MutationState } from "./hooks.js";
export { DataClient, SdkError } from "./client.js";
export type { QueryResponse, TraceTree, ClientConfig, QueryInput, ScoreInput } from "./client.js";

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

// Re-export the design system so a plugin has a single import surface.
export * from "@llmobs/ui";
export * from "@llmobs/tokens";
