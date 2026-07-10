// @llmobs/tokens — typed accessors over the CSS custom properties in tokens.css.
// Import the CSS once at the app root: `import "@llmobs/tokens/tokens.css"`.

export type Theme = "light" | "dark";

/** var(name) helper so TS callers reference tokens without stringly-typing. */
export function token(name: string): string {
  return `var(--llmobs-${name})`;
}

/** Resolve the active theme, honoring a manual override then the OS preference. */
export function getTheme(): Theme {
  if (typeof document !== "undefined") {
    const attr = document.documentElement.getAttribute("data-theme");
    if (attr === "light" || attr === "dark") return attr;
  }
  if (typeof window !== "undefined" && window.matchMedia?.("(prefers-color-scheme: dark)").matches) {
    return "dark";
  }
  return "light";
}

/** Stamp the theme on <html>. The shell persists the choice via the SDK kv, not
 *  localStorage, so it is passed in and applied here. */
export function applyTheme(theme: Theme): void {
  if (typeof document !== "undefined") {
    document.documentElement.setAttribute("data-theme", theme);
  }
}

export function toggleTheme(): Theme {
  const next: Theme = getTheme() === "dark" ? "light" : "dark";
  applyTheme(next);
  return next;
}

/** Canonical span-kind → badge hue token. Unknown kinds fall back to `span`. */
export const kindHue: Record<string, string> = {
  generation: token("hue-generation"),
  tool_call: token("hue-tool"),
  agent_step: token("hue-agent"),
  retrieval: token("hue-retrieval"),
  embedding: token("hue-retrieval"),
  span: token("hue-span"),
};

export function hueForKind(kind: string): string {
  return kindHue[kind] ?? token("hue-span");
}
