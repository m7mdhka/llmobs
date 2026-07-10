// TypeScript mirror of api/schemas/manifest/v1alpha1/llmobs-plugin.schema.json.
// Kept in sync with that schema (the schema is the contract; this is DX sugar).

export type Capability = "ingest" | "query" | "write" | "events" | "jobs" | "kv" | "secrets" | "surface";

export interface PluginNavItem {
  path: string;
  label: string;
  section?: string;
}

export interface PluginFrontend {
  remoteName: string;
  exposedModule: string;
  entry: string;
  nav: PluginNavItem[];
}

// Tier-3 backend service (external-URL executor, ADR-0023/R2). The operator runs
// the service; the kernel handshakes, supervises, and proxies /api/plugins/{id}/*.
export interface PluginBackend {
  url: string;
  healthPath: string;
  infoPath?: string;
  watermarkBudget?: string; // e.g. "30s"
}

// Plugin-owned structured collections (the `store` primitive, ADR-0023/R1+R4).
export interface PluginCollectionField {
  name: string;
  type: "string" | "number" | "boolean" | "json";
  indexed?: boolean;
}
export interface PluginCollection {
  name: string;
  fields: PluginCollectionField[];
}
export interface PluginStore {
  collections: PluginCollection[];
}

// Scheduled/on-demand jobs (the `jobs` primitive, H6b). Absent schedule => on-demand.
export interface PluginJob {
  name: string;
  schedule?: string; // 5-field cron or "@every <duration>"
  path: string; // plugin backend endpoint
  maxAttempts?: number;
}

export interface PluginManifest {
  apiVersion: "llmobs.dev/v1alpha1";
  kind: "Plugin";
  metadata: {
    id: string; // owner/plugin-name
    name: string;
    version: string;
    description?: string;
  };
  spec: {
    capabilities: Capability[];
    permissions?: string[];
    frontend?: PluginFrontend;
    settingsSchema?: string;
    backend?: PluginBackend;
    store?: PluginStore;
    jobs?: PluginJob[];
  };
}
