import React, { useEffect, useMemo, useRef, useState } from "react";
import { LoadingState, PluginUnavailable } from "@llmobs/ui";
import { DataClient, type PluginMountContext, type PluginUnmount } from "@llmobs/plugin-sdk";
import { currentTheme } from "./theme.js";
import { loadPluginMount } from "./remoteLoader.js";
import { makeFrontendTokenProvider, type RegistryPlugin, type Session } from "./api.js";

// Mounts a plugin's federated surface through the FRAMEWORK-NEUTRAL contract (ADR-0030):
// the shell builds a plain `context`, loads the remote's `mount(element, context)`, calls
// it into a container it owns, and calls the returned `unmount()` on route change. The
// plugin renders with any framework — the shell no longer renders a React component or
// wraps it in a React provider. A failure to load or a throw from `mount` degrades to the
// standard unavailable state; a broken plugin never takes down the shell.
export function PluginRoute({
  plugin,
  session,
  basePath,
}: {
  plugin: RegistryPlugin;
  session: Session;
  basePath: string;
}): React.ReactElement {
  const containerRef = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState<"loading" | "ready" | "failed">("loading");

  // The neutral mount context. Stable across renders (memoized on plugin id + session)
  // so the mount effect runs once per surface. The data client is built HERE, in the
  // shell, under the plugin's frontend token — so its G1 fail-closed enforcement holds
  // for whatever framework the plugin uses, exactly as it did for the React binding.
  const context = useMemo<PluginMountContext>(() => {
    // The shell OWNS the frontend-token provider and closes over it inside the data
    // client — it is deliberately NOT placed on the context, so untrusted plugin code
    // cannot read the raw bearer token and exfiltrate it off-origin. G1 confinement still
    // holds: the client fails closed when no token can be minted.
    const frontendToken = makeFrontendTokenProvider(plugin.id, session.csrfToken, () => undefined);
    const theme = currentTheme() === "dark" ? "dark" : "light";
    return {
      baseUrl: "",
      basePath,
      user: { id: session.user.id, email: session.user.email, role: session.user.role },
      client: new DataClient({ baseUrl: "", frontendToken }),
      theme: { mode: theme },
      // Locale/direction are threaded now (N1); N3 wires real detection + RTL.
      locale: "en",
      direction: "ltr",
    };
  }, [plugin.id, basePath, session.csrfToken, session.user.id, session.user.email, session.user.role]);

  useEffect(() => {
    let alive = true;
    let unmount: PluginUnmount | void;
    setStatus("loading");
    loadPluginMount(plugin)
      .then((mount) => {
        if (!alive || !containerRef.current) return;
        try {
          unmount = mount(containerRef.current, context);
          if (alive) setStatus("ready");
        } catch {
          if (alive) setStatus("failed");
        }
      })
      .catch(() => {
        if (alive) setStatus("failed");
      });
    return () => {
      alive = false;
      if (typeof unmount === "function") {
        try {
          unmount();
        } catch {
          /* a plugin's teardown throwing must not break navigation */
        }
      }
    };
  }, [plugin.id, context]);

  if (status === "failed") {
    return <PluginUnavailable pluginName={plugin.name} reason="The plugin's frontend failed to load." />;
  }
  // The container is always present so `mount` has a stable element to render into; the
  // loading indicator overlays until mount resolves.
  return (
    <>
      {status === "loading" && <LoadingState title={`Loading ${plugin.name}…`} />}
      <div ref={containerRef} data-plugin={plugin.id} />
    </>
  );
}
