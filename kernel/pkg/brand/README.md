# pkg/brand — the one place the product name lives (Go)

D15: **Rename stays cheap.** The product name appears in exactly one constant per
language. In Go, that is this package. In TypeScript, it is `packages/brand`.

Never hardcode "llmobs" in strings, table names, or env prefixes anywhere else —
derive them from the constant exported here. When the product is renamed, this
package (and its TS twin) is the only place that changes.
