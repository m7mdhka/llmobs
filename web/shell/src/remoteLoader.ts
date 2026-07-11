// Dynamic Module Federation remote loading. The shell registers plugin remotes
// from the registry at runtime and loads their exposed module on demand. The
// host hardcodes no remotes — this is the whole point of the platform.
//
// N1 (ADR-0030): a remote's exposed module is FRAMEWORK-NEUTRAL — it exports a
// `mount(element, context) -> unmount` function, not a React component. The shell calls
// `mount` into a container it owns; the plugin renders with any framework. MF remains
// only the transport (dynamic remotes), never a React coupling.
import { registerRemotes, loadRemote } from "@module-federation/enhanced/runtime";
import type { PluginMount } from "@llmobs/plugin-sdk";
import type { RegistryPlugin } from "./api.js";

/** Register the registry's plugins as MF remotes at runtime. The host runtime is
 *  already initialized by the ModuleFederationPlugin (build time); we only add
 *  remotes here — using init() would reset the host. `force` lets re-registration
 *  update an existing remote. */
export function registerPlugins(plugins: RegistryPlugin[]): void {
  const remotes = plugins.map((p) => ({ name: p.remoteName, entry: p.remoteEntry }));
  if (remotes.length > 0) {
    registerRemotes(remotes, { force: true });
  }
}

/** The neutral shape a plugin's exposed module must export (ADR-0030). */
export interface PluginModule {
  mount: PluginMount;
}

/** Load a plugin's exposed neutral mount function. Throws on failure so the caller can
 *  render the standard unavailable state (a broken plugin never takes down the shell). */
export async function loadPluginMount(plugin: RegistryPlugin): Promise<PluginMount> {
  const mod = (await loadRemote(`${plugin.remoteName}/${stripDotSlash(plugin.exposedModule)}`)) as PluginModule | null;
  if (!mod || typeof mod.mount !== "function") {
    throw new Error(
      `plugin ${plugin.id} exposed no mount() — a frontend must export mount(element, context) (ADR-0030)`,
    );
  }
  return mod.mount;
}

function stripDotSlash(m: string): string {
  return m.startsWith("./") ? m.slice(2) : m;
}
