import React, { Component, Suspense, useEffect, useMemo, useState, type ComponentType, type ReactNode } from "react";
import { LoadingState, PluginUnavailable } from "@llmobs/ui";
import { LLMObsPluginProvider } from "@llmobs/plugin-sdk";
import { loadPluginSurface } from "./remoteLoader.js";
import { makeFrontendTokenProvider, type RegistryPlugin, type Session } from "./api.js";

// Mounts a plugin's federated surface inside the SDK provider (which supplies the
// project + user + data client). A failure to load the remote (network,
// integrity, missing export) degrades to the standard unavailable state — a
// broken plugin never takes down the shell (the dogfood / graceful-degradation
// rule). Each render of a route gets a fresh boundary keyed by plugin id.
export function PluginRoute({ plugin, session }: { plugin: RegistryPlugin; session: Session }): React.ReactElement {
  const [surface, setSurface] = useState<ComponentType<Record<string, never>> | null>(null);
  const [failed, setFailed] = useState(false);

  // J1: a per-plugin, per-session frontend token provider. Stable across renders
  // (memoized on plugin id + session) so the SDK reuses one caching provider and
  // re-mints only near expiry. Confines a cooperating frontend to its grant; a
  // hostile frontend can still bypass it same-origin (documented trust model).
  const frontendToken = useMemo(
    () => makeFrontendTokenProvider(plugin.id, session.csrfToken, () => undefined),
    [plugin.id, session.csrfToken],
  );

  useEffect(() => {
    let alive = true;
    setSurface(null);
    setFailed(false);
    loadPluginSurface(plugin)
      .then((c) => alive && setSurface(() => c))
      .catch(() => alive && setFailed(true));
    return () => {
      alive = false;
    };
  }, [plugin.id]);

  if (failed) {
    return <PluginUnavailable pluginName={plugin.name} reason="The plugin's frontend failed to load." />;
  }
  if (!surface) {
    return <LoadingState title={`Loading ${plugin.name}…`} />;
  }
  const Surface = surface;
  return (
    <PluginErrorBoundary pluginName={plugin.name}>
      <Suspense fallback={<LoadingState title={`Loading ${plugin.name}…`} />}>
        <LLMObsPluginProvider config={{ user: session.user, frontendToken }}>
          <Surface />
        </LLMObsPluginProvider>
      </Suspense>
    </PluginErrorBoundary>
  );
}

class PluginErrorBoundary extends Component<{ pluginName: string; children: ReactNode }, { crashed: boolean }> {
  constructor(props: { pluginName: string; children: ReactNode }) {
    super(props);
    this.state = { crashed: false };
  }
  static getDerivedStateFromError(): { crashed: boolean } {
    return { crashed: true };
  }
  render(): ReactNode {
    if (this.state.crashed) {
      return <PluginUnavailable pluginName={this.props.pluginName} reason="The plugin crashed while rendering." />;
    }
    return this.props.children;
  }
}
