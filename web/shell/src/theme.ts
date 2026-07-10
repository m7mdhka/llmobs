import { applyTheme, getTheme, type Theme } from "@llmobs/tokens";

// Theme persistence: the shell may persist to the SDK kv later; for the host's
// own chrome we use a cookie-free in-memory + <html> attribute approach. The
// initial theme follows the OS unless the user toggles within the session.
let current: Theme | null = null;

export function initTheme(): void {
  current = getTheme();
  applyTheme(current);
}

export function currentTheme(): Theme {
  return current ?? getTheme();
}

export function setTheme(next: Theme): void {
  current = next;
  applyTheme(next);
}
