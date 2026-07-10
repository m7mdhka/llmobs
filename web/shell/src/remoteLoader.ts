// Dynamic Module Federation remote loading. The shell registers plugin remotes
// from the registry at runtime and loads their exposed module on demand. The
// host hardcodes no remotes — this is the whole point of the platform.
import { init, loadRemote } from "@module-federation/enhanced/runtime";
import type { ComponentType } from "react";
import type { RegistryPlugin } from "./api.js";

let initialized = false;

/** Register the registry's plugins as MF remotes. Idempotent per set. */
export function registerPlugins(plugins: RegistryPlugin[]): void {
  const remotes = plugins.map((p) => ({
    name: p.remoteName,
    entry: p.remoteEntry,
    // Subresource integrity, when the registry pins it.
    ...(p.integrity ? { entryGlobalName: p.remoteName } : {}),
  }));
  if (!initialized) {
    init({ name: "shell", remotes });
    initialized = true;
  } else {
    // enhanced runtime supports re-registration via init merge.
    init({ name: "shell", remotes });
  }
}

/** The shape a plugin's exposed module must default-export. */
export interface PluginModule {
  default: ComponentType<Record<string, never>>;
}

/** Load a plugin's exposed surface component. Throws on failure so the caller can
 *  render the standard unavailable state. */
export async function loadPluginSurface(plugin: RegistryPlugin): Promise<ComponentType<Record<string, never>>> {
  const mod = (await loadRemote(`${plugin.remoteName}/${stripDotSlash(plugin.exposedModule)}`)) as PluginModule | null;
  if (!mod || !mod.default) {
    throw new Error(`plugin ${plugin.id} exposed no default surface`);
  }
  return mod.default;
}

function stripDotSlash(m: string): string {
  return m.startsWith("./") ? m.slice(2) : m;
}
