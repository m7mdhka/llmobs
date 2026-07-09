# @llmobs/brand — the one place the product name lives (TS)

D15 twin of `kernel/pkg/brand`. The product name appears in exactly one constant
per language; in TypeScript, that is this package.

Never hardcode "llmobs" in UI strings, storage keys, or config prefixes — import
the constant from here. A rename changes this package and its Go twin, nothing
else.
