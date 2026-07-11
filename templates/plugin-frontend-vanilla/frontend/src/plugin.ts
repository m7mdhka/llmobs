import { createTranslator, type PluginMount } from "@llmobs/plugin-sdk";

// The i18n seam (N3): source strings live here as fallbacks; a deployment localizes by
// adding a catalog for the target locale — no code change at the call sites. RTL locales
// also flip layout automatically because we set `dir` from context.direction below.
const messages = {
  ar: { title: "عمليات التتبع الأخيرة (فانيلا، بدون إطار عمل)", loading: "جار التحميل…" },
};

// A FRAMEWORK-FREE plugin surface — no React, no framework at all, just the neutral
// mount contract (ADR-0030) + DOM. This is the falsification of "any framework": if the
// shell can mount and drive this with zero React, the contract is genuinely neutral. The
// shell calls `mount(element, context)`; we render with `document` APIs, query through
// the token-confined `context.client`, and return an idempotent `unmount`.
export const mount: PluginMount = (element, context) => {
  const t = createTranslator(messages, context.locale);
  const root = document.createElement("section");
  root.className = "vanilla-plugin";
  // Locale-aware: flip layout for RTL locales via the shell-supplied direction, and pick
  // localized strings through the seam (falling back to the source text).
  root.setAttribute("dir", context.direction);

  const heading = document.createElement("h1");
  heading.textContent = t("title", "Recent traces (vanilla, no framework)");

  const status = document.createElement("p");
  status.className = "status";
  status.textContent = t("loading", "Loading…");

  const list = document.createElement("ul");
  list.className = "trace-list";

  root.append(heading, status, list);
  element.appendChild(root);

  const controller = new AbortController();
  const now = new Date();
  const from = new Date(now.getTime() - 24 * 60 * 60 * 1000);

  context.client
    .query(
      { target: "traces", timeRange: { from: from.toISOString(), to: now.toISOString() }, limit: 20 },
      controller.signal,
    )
    .then((res) => {
      status.textContent = `${res.data.length} trace(s)`;
      for (const row of res.data as Array<{ id?: string; name?: string }>) {
        const li = document.createElement("li");
        li.textContent = row.name ?? row.id ?? "(unnamed)";
        list.appendChild(li);
      }
    })
    .catch((err: unknown) => {
      // The token-confined client FAILS CLOSED (G1): with no frontend token it throws
      // rather than calling the kernel at the user's full session scope. A non-React
      // caller is confined IDENTICALLY — we surface the error and never over-reach.
      const code = (err as { code?: string })?.code ?? (err as Error)?.message ?? "error";
      status.textContent = `Failed to load: ${code}`;
      status.setAttribute("data-error", code);
    });

  // Idempotent teardown: cancel the in-flight query and remove all of our DOM.
  let torn = false;
  return () => {
    if (torn) return;
    torn = true;
    controller.abort();
    root.remove();
  };
};
