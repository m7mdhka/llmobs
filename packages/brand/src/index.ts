// @llmobs/brand — the ONLY place the user-visible product name lives on the web
// side (D15, mirror of kernel/pkg/brand). Everything else reads `brand` from
// here, so a deployment can white-label by injecting overrides — no fork.

export interface Brand {
  /** Product display name shown in the topbar, login, and tab title. */
  name: string;
  /** Documentation URL used by help links. */
  docsUrl: string;
  /** Optional logo mark URL (data: or same-origin). When unset the shell renders
   *  its built-in gradient mark. */
  logoUrl?: string;
}

// The default identity. This is the one intentional occurrence of the display
// name in the web codebase; the CI brand-guard allows it only here.
const DEFAULT_BRAND: Brand = {
  name: "LLMObs",
  docsUrl: "https://llmobs.dev/docs",
};

// A deployment overrides the identity by setting window.__LLMOBS_BRAND__ (the
// kernel can inject it, or an operator can via a small script) — white-label is a
// runtime deployment concern, not a rebuild.
export function resolveBrand(): Brand {
  const override =
    typeof globalThis !== "undefined" && (globalThis as { __LLMOBS_BRAND__?: Partial<Brand> }).__LLMOBS_BRAND__;
  return { ...DEFAULT_BRAND, ...(override ?? {}) };
}

/** The resolved brand for this session. */
export const brand: Brand = resolveBrand();
