// Bundle an OpenAPI 3.1 spec into an oapi-codegen-loadable 3.0.3 form.
//
// The canonical OpenAPI (api/openapi/v1alpha1/query.yaml) $refs the full JSON
// Schema 2020-12 model schemas and the DSL schema as the single source of truth.
// oapi-codegen's loader (kin-openapi) rejects 2020-12 constructs. This step is
// used ONLY to generate the Go server interface (operations + routing + envelope
// types); the rich entity/DSL types are generated separately by quicktype and
// validated at runtime against the original JSON Schemas. So every external $ref
// is replaced with an OPAQUE object; `const` is lowered to `enum`; JSON-Schema-only
// keywords are dropped; the doc is emitted as OpenAPI 3.0.3. The bundle is a build
// artifact (git-ignored).
//
// Usage: node bundle_openapi.mjs <in.yaml> <out.json>
import { readFileSync, writeFileSync } from "node:fs";
import { parse } from "yaml";

const DROP = new Set(["$schema", "$id", "$comment", "unevaluatedProperties", "if", "then", "else"]);

function walk(node) {
  if (Array.isArray(node)) return node.map(walk);
  if (node === null || typeof node !== "object") return node;
  const ref = node["$ref"];
  if (typeof ref === "string" && ref && !ref.startsWith("#")) {
    return { type: "object", description: `opaque; see ${ref} (generated types)` };
  }
  const out = {};
  for (const [k, v] of Object.entries(node)) {
    if (DROP.has(k) || k.startsWith("x-llmobs")) continue;
    if (k === "const") { out.enum = [v]; continue; }
    out[k] = walk(v);
  }
  return out;
}

const [src, dst] = process.argv.slice(2);
const doc = walk(parse(readFileSync(src, "utf8")));
doc.openapi = "3.0.3";
writeFileSync(dst, JSON.stringify(doc, null, 2));
console.log(`bundled ${src} -> ${dst} (openapi 3.0.3, external refs opaqued)`);
