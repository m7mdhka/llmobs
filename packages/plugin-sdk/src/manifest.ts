// TypeScript mirror of api/schemas/manifest/v1alpha1/llmobs-plugin.schema.json.
// Kept in sync with that schema (the schema is the contract; this is DX sugar).

export type Capability = "query" | "write" | "events" | "jobs" | "kv" | "secrets" | "surface";

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
  };
}
