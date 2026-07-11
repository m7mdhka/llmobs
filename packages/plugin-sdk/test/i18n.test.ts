import { describe, it, expect, beforeEach } from "vitest";
// The i18n seam + locale/direction helpers are part of the NEUTRAL SDK root (N3), so a
// plugin in any framework imports them from "@llmobs/plugin-sdk". Test the built dist.
import { createTranslator, directionForLocale, applyLocale, getLocale } from "../dist/index.js";

describe("directionForLocale (N3)", () => {
  it("maps RTL primary subtags to rtl and everything else to ltr", () => {
    expect(directionForLocale("en")).toBe("ltr");
    expect(directionForLocale("en-US")).toBe("ltr");
    expect(directionForLocale("ar")).toBe("rtl");
    expect(directionForLocale("ar-EG")).toBe("rtl"); // region subtag ignored
    expect(directionForLocale("he")).toBe("rtl");
    expect(directionForLocale("fa_IR")).toBe("rtl"); // underscore separator
    expect(directionForLocale("")).toBe("ltr"); // empty → ltr default
  });
});

describe("applyLocale + getLocale (N3)", () => {
  beforeEach(() => {
    document.documentElement.removeAttribute("lang");
    document.documentElement.removeAttribute("dir");
  });

  it("stamps lang + dir on the document root and derives direction from the locale", () => {
    applyLocale("ar-EG");
    expect(document.documentElement.getAttribute("lang")).toBe("ar-EG");
    expect(document.documentElement.getAttribute("dir")).toBe("rtl");
    expect(getLocale()).toBe("ar-EG"); // an explicit <html lang> override wins
  });

  it("an explicit direction overrides the locale default", () => {
    applyLocale("en", "rtl");
    expect(document.documentElement.getAttribute("dir")).toBe("rtl");
  });
});

describe("createTranslator — the externalization seam (N3)", () => {
  const catalog = {
    ar: { save: "حفظ", traces: "عمليات التتبع" },
    "ar-EG": { save: "احفظ" }, // an exact-locale override of the primary subtag
  };

  it("resolves exact locale → primary subtag → fallback", () => {
    const tEn = createTranslator(catalog, "en");
    expect(tEn("save", "Save")).toBe("Save"); // no catalog → source string, never empty

    const tAr = createTranslator(catalog, "ar");
    expect(tAr("save", "Save")).toBe("حفظ"); // exact locale
    expect(tAr("missing", "Fallback")).toBe("Fallback"); // key absent → fallback

    const tArEg = createTranslator(catalog, "ar-EG");
    expect(tArEg("save", "Save")).toBe("احفظ"); // exact ar-EG override
    expect(tArEg("traces", "Traces")).toBe("عمليات التتبع"); // falls back to primary "ar"
  });
});

// The design system must stay RTL-ready: physical-direction CSS (left/right) does NOT flip
// for RTL, logical properties (inline-start/end, text-align:start/end) do. This guard fails
// if a physical-direction property creeps back into @llmobs/ui, so "components flip under
// RTL" (N3) can't silently regress.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

describe("packages/ui uses logical CSS properties (RTL-ready, N3)", () => {
  it("has no physical-direction properties", () => {
    // vitest runs with cwd = packages/plugin-sdk; @llmobs/ui is a sibling.
    const cssPath = resolve(process.cwd(), "../ui/src/ui.css");
    const css = readFileSync(cssPath, "utf8");
    // Strip the one legitimate direction-neutral centering idiom (left:50% + translate).
    const offenders = css.match(
      /(margin|padding|border)-(left|right)\s*:|text-align\s*:\s*(left|right)\b/gi,
    );
    expect(offenders ?? []).toEqual([]);
  });
});
