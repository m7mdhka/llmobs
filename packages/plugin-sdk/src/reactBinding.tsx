// The React binding over the neutral mount contract (Arc N / N1, ADR-0030 R4).
// `createReactBinding(Root)` turns a React root component into a neutral `mount`
// function: it creates a React root on the shell-owned element, wraps the component in
// the SDK provider seeded from the neutral `context`, and returns `root.unmount` as the
// neutral `unmount`. React is thus ONE binding — a Vue/Svelte/vanilla plugin never
// imports this file (and never shares the react/react-dom singletons it needs).
import * as React from "react";
import { createRoot, type Root } from "react-dom/client";
import type { PluginMount, PluginMountContext, PluginUnmount } from "./mount.js";
import { LLMObsPluginProvider } from "./context.js";

/**
 * Adapt a React root component to the neutral mount contract. The component renders
 * inside `LLMObsPluginProvider`, so the existing hooks (useTraces, …) work unchanged —
 * the provider is seeded with the SAME token-confined client the shell built, so the G1
 * frontend-token enforcement is identical whether reached via a hook or `context.client`
 * directly.
 *
 * The Root component receives the neutral `context` as a prop (for `basePath`, theme,
 * locale) AND can use the hooks (it renders inside the provider). Usage in a plugin entry:
 *   export const mount = createReactBinding(TracingApp);
 *   function TracingApp({ context }: { context: PluginMountContext }) {
 *     return <BrowserRouter basename={context.basePath}>…</BrowserRouter>;
 *   }
 */
export function createReactBinding(Root: React.ComponentType<{ context: PluginMountContext }>): PluginMount {
  return (element: HTMLElement, context: PluginMountContext): PluginUnmount => {
    const root: Root = createRoot(element);
    root.render(
      <React.StrictMode>
        <ReactPluginBoundary>
          <LLMObsPluginProvider
            config={{
              baseUrl: context.baseUrl,
              projectId: context.project?.id,
              user: context.user,
              // Reuse the shell-built, token-confined client — do NOT rebuild one (that
              // would risk a second client without the frontend token). The raw token
              // provider is intentionally not on the context (least exposure); the client
              // already carries the confinement.
              client: context.client,
            }}
          >
            <Root context={context} />
          </LLMObsPluginProvider>
        </ReactPluginBoundary>
      </React.StrictMode>,
    );
    // Idempotent teardown: React tolerates a double unmount; guard so the shell can call
    // it more than once safely.
    let done = false;
    return () => {
      if (done) return;
      done = true;
      root.unmount();
    };
  };
}

// Crash containment for the React binding: because the shell mounts the plugin into its
// OWN React root (not the shell's tree), the shell's error boundary can't catch a render
// error inside the plugin. This boundary keeps a React plugin's crash from leaving a
// blank surface — it renders a minimal, framework-local fallback instead.
class ReactPluginBoundary extends React.Component<{ children: React.ReactNode }, { crashed: boolean }> {
  constructor(props: { children: React.ReactNode }) {
    super(props);
    this.state = { crashed: false };
  }
  static getDerivedStateFromError(): { crashed: boolean } {
    return { crashed: true };
  }
  render(): React.ReactNode {
    if (this.state.crashed) {
      return (
        <div role="alert" style={{ padding: "2rem", color: "var(--color-text-muted, #888)" }}>
          This plugin crashed while rendering.
        </div>
      );
    }
    return this.props.children;
  }
}
