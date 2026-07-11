// @llmobs/plugin-sdk/react — the React BINDING over the neutral SDK core (Arc N / N1,
// ADR-0030 R4). A React plugin imports this; a Vue/Svelte/vanilla plugin imports only
// `@llmobs/plugin-sdk`. This is a thin adapter: the provider + hooks read the same
// neutral primitives (DataClient, the frontend token) the neutral contract exposes, and
// `createReactBinding` turns a React component into a neutral `mount`. The design system
// (@llmobs/ui) + SchemaForm are re-exported here so a React plugin keeps a single import.

// The neutral→React adapter: turn a React root component into a neutral `mount`.
export { createReactBinding } from "./reactBinding.js";

// Provider + context (the React binding seeds it from the neutral mount context).
export { LLMObsPluginProvider, useLLMObs } from "./context.js";
export type { LLMObsContextValue, ProviderConfig } from "./context.js";

// Data hooks over `query` + `write` (thin wrappers over the neutral DataClient).
export { useQuery, useTraces, useTrace, useWriteScore, useSettings } from "./hooks.js";
export type { AsyncState, TracesParams, MutationState, SettingsState } from "./hooks.js";

// The design system (React components) + SchemaForm — React-only, so they live here.
export * from "@llmobs/ui";
export { SchemaForm } from "@llmobs/schema-form";
export type { SchemaFormProps } from "@llmobs/schema-form";
