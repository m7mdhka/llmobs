import { applyTheme, applyLocale, detectLocale, directionForLocale, getLocale, getTheme, type Direction, type Theme } from "@llmobs/tokens";

// Theme persistence: the shell may persist to the SDK kv later; for the host's
// own chrome we use a cookie-free in-memory + <html> attribute approach. The
// initial theme follows the OS unless the user toggles within the session.
let current: Theme | null = null;
let currentLoc: string | null = null;

export function initTheme(): void {
  current = getTheme();
  applyTheme(current);
}

// Locale + direction (N3): resolve the active locale (browser preference or an <html
// lang> override) and stamp `lang` + `dir` on the document root so CSS logical properties
// and native text rendering flip. Wired into the plugin mount context by PluginRoute, so
// a plugin gets the same locale the shell chrome uses.
export function initLocale(): void {
  // Detect the BROWSER preference (not the static <html lang> a11y default) so an ar/he
  // browser actually gets RTL, then stamp lang+dir over the default.
  currentLoc = detectLocale();
  applyLocale(currentLoc);
}

export function currentLocale(): string {
  return currentLoc ?? getLocale();
}

export function currentDirection(): Direction {
  return directionForLocale(currentLocale());
}

export function currentTheme(): Theme {
  return current ?? getTheme();
}

export function setTheme(next: Theme): void {
  current = next;
  applyTheme(next);
}
