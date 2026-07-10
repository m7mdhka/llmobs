import * as React from "react";
import { DataClient } from "./client.js";

// The runtime context the shell provides to every plugin surface: which project
// is active, the gateway base URL, and the signed-in user. A plugin reads it via
// useLLMObs()/hooks and never reaches around it — the dogfood boundary.
export interface LLMObsContextValue {
  projectId?: string;
  baseUrl: string;
  user?: { id: string; email: string; role: string };
  client: DataClient;
}

const LLMObsContext = React.createContext<LLMObsContextValue | null>(null);

export interface ProviderConfig {
  /** Gateway base URL; default same origin. */
  baseUrl?: string;
  projectId?: string;
  user?: { id: string; email: string; role: string };
  /** Test seam. */
  fetchImpl?: typeof fetch;
}

/** The shell wraps each plugin surface in this. Plugins never render it. */
export function LLMObsPluginProvider({
  config,
  children,
}: {
  config: ProviderConfig;
  children: React.ReactNode;
}): React.ReactElement {
  const value = React.useMemo<LLMObsContextValue>(() => {
    const baseUrl = config.baseUrl ?? "";
    return {
      baseUrl,
      projectId: config.projectId,
      user: config.user,
      client: new DataClient({ baseUrl, projectId: config.projectId, fetchImpl: config.fetchImpl }),
    };
  }, [config.baseUrl, config.projectId, config.user, config.fetchImpl]);

  return <LLMObsContext.Provider value={value}>{children}</LLMObsContext.Provider>;
}

/** Access the plugin context. Throws if used outside a provider (a plugin
 *  mounted incorrectly), which surfaces as the plugin's error boundary. */
export function useLLMObs(): LLMObsContextValue {
  const ctx = React.useContext(LLMObsContext);
  if (!ctx) {
    throw new Error("useLLMObs must be used within an LLMObsPluginProvider (mount your surface through the shell)");
  }
  return ctx;
}
