// Tiny classname joiner (no dependency). Falsy entries are dropped.
export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(" ");
}
