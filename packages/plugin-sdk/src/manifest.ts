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
  };
}
