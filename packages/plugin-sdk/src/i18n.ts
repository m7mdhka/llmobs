// The string-externalization SEAM (Arc N / N3). This is the MECHANISM to localize a
// plugin's strings, not a set of translations: a plugin defines its source strings once,
// looks them up by the mount context's locale, and a deployment localizes by dropping in a
// catalog for the target locale. Framework-neutral (no React) — a Vue/Svelte/vanilla
// plugin uses it exactly like a React one. Kept deliberately tiny (no ICU MessageFormat,
// no locale-data dependency on the frontend) — the point is the seam, not a full i18n lib.

/** A message catalog: locale (BCP-47) → message key → localized string. A plugin ships
 *  its source strings as the fallback and, optionally, catalogs for other locales. */
export type MessageCatalog = Record<string, Record<string, string>>;

/** A translator bound to a locale: `t(key, fallback)` returns the localized string for
 *  `key`, else the `fallback` (the plugin's source string). The fallback is REQUIRED so a
 *  missing translation degrades to readable source text, never an empty or key-looking
 *  string. */
export type Translator = (key: string, fallback: string) => string;

/**
 * createTranslator binds a catalog to a locale. Lookup order: the exact locale (e.g.
 * "ar-EG"), then its primary subtag (e.g. "ar"), then the caller's fallback. So a plugin
 * writes `t("save", "Save")` at every call site; a deployment localizes by adding
 * `{ "ar": { "save": "حفظ" } }` to the catalog — no plugin code change.
 */
export function createTranslator(catalog: MessageCatalog, locale: string): Translator {
  const exact = catalog[locale];
  const primary = catalog[(locale || "").toLowerCase().split(/[-_]/)[0]];
  return (key, fallback) => exact?.[key] ?? primary?.[key] ?? fallback;
}
