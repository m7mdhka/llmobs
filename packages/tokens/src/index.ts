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

// --- Locale + text direction (N3) --------------------------------------------------
// Direction is a first-class part of the theme: the shell stamps `lang` + `dir` on the
// document root, `packages/ui` uses logical CSS properties so components flip
// automatically, and the plugin mount context (ADR-0030) carries locale + direction so a
// plugin lays itself out correctly in any framework.

export type Direction = "ltr" | "rtl";

// The right-to-left language subtags (BCP-47 primary subtag). A locale like "ar-EG" or
// "he" is RTL; everything else is LTR. Kept as a small explicit set (the RTL scripts are
// few and stable) rather than pulling a locale-data dependency onto the frontend.
const rtlLangs = new Set(["ar", "arc", "dv", "fa", "ha", "he", "khw", "ks", "ku", "ps", "sd", "ug", "ur", "yi"]);

/** The text direction for a BCP-47 locale (e.g. "ar-EG" → "rtl", "en" → "ltr"). */
export function directionForLocale(locale: string): Direction {
  const primary = (locale || "").toLowerCase().split(/[-_]/)[0];
  return rtlLangs.has(primary) ? "rtl" : "ltr";
}

/** Resolve the active locale: the currently-stamped <html lang> (set by applyLocale at
 *  boot / on change), else the browser preference, else "en". Read this AFTER boot to get
 *  the resolved locale (a plugin does; the shell's mount context uses it). */
export function getLocale(): string {
  if (typeof document !== "undefined") {
    const lang = document.documentElement.getAttribute("lang");
    if (lang) return lang;
  }
  return detectLocale();
}

/** DETECT the browser's preferred locale, independent of any stamped <html lang>. The
 *  shell calls this ONCE at boot to seed applyLocale — the static document ships a `lang`
 *  default for the pre-JS paint (a11y), so detection must NOT read that default back, or
 *  the browser preference would be unreachable. */
export function detectLocale(): string {
  if (typeof navigator !== "undefined" && navigator.language) return navigator.language;
  return "en";
}

/** Stamp `lang` + `dir` on <html> so CSS logical properties and native text rendering
 *  flip for the locale. Direction defaults to directionForLocale(locale). */
export function applyLocale(locale: string, direction: Direction = directionForLocale(locale)): void {
  if (typeof document !== "undefined") {
    document.documentElement.setAttribute("lang", locale);
    document.documentElement.setAttribute("dir", direction);
  }
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
