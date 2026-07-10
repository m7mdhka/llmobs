import * as React from "react";
import { DataClient, type FrontendTokenProvider, type LLMObsClient } from "./client.js";

// The runtime context the shell provides to every plugin surface: which project
// is active, the gateway base URL, and the signed-in user. A plugin reads it via
// useLLMObs()/hooks and never reaches around it — the dogfood boundary.
export interface LLMObsContextValue {
  projectId?: string;
  baseUrl: string;
  user?: { id: string; email: string; role: string };
  client: LLMObsClient;
}

const LLMObsContext = React.createContext<LLMObsContextValue | null>(null);

export interface ProviderConfig {
  /** Gateway base URL; default same origin. */
  baseUrl?: string;
  projectId?: string;
  user?: { id: string; email: string; role: string };
  /**
   * Plugin frontend token provider (J1). The shell passes a getter that caches +
   * refreshes this plugin's short-TTL frontend token; the SDK's data hooks present
   * it transparently so the plugin's Query API calls run at least-privilege
   * (plugin-grant ∩ session ∩ project) instead of the full session. Least-privilege
   * by default — see the FrontendTokenProvider note; not a boundary against a
   * hostile frontend.
   */
  frontendToken?: FrontendTokenProvider;
  /** Test seam. */
  fetchImpl?: typeof fetch;
  /**
   * Inject a pre-built data client instead of constructing one from baseUrl. The
   * testing utilities (@llmobs/plugin-sdk/testing) use this to supply a fake client;
   * production leaves it unset and a real DataClient is built.
   */
  client?: LLMObsClient;
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
      client:
        config.client ??
        new DataClient({
          baseUrl,
          projectId: config.projectId,
          fetchImpl: config.fetchImpl,
          frontendToken: config.frontendToken,
        }),
    };
  }, [config.baseUrl, config.projectId, config.user, config.fetchImpl, config.frontendToken, config.client]);

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
