// Dereference a JSON Schema into a self-contained document for quicktype.
//
// quicktype's cross-file $ref / $id resolution is environment-sensitive (it can
// try to resolve a schema's absolute $id URL, which fails offline / varies by
// node version). To make codegen deterministic everywhere, we inline every $ref
// (local "#/$defs/..." and external "<file>#/$defs/...") into a self-contained
// schema and strip $id/$schema before feeding quicktype.
//
// Usage: node deref_schema.mjs <in.schema.json> <out.schema.json>
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";

const cache = new Map();
function loadFile(p) {
  if (!cache.has(p)) cache.set(p, JSON.parse(readFileSync(p, "utf8")));
  return cache.get(p);
}

function pointer(doc, frag) {
  // frag like "/$defs/attributeMap"
  let cur = doc;
  for (const raw of frag.split("/").slice(1)) {
    const key = raw.replace(/~1/g, "/").replace(/~0/g, "~");
    cur = cur[key];
    if (cur === undefined) throw new Error(`unresolved pointer ${frag}`);
  }
  return cur;
}

function deref(node, baseDir, rootDoc, stack) {
  if (Array.isArray(node)) return node.map((x) => deref(x, baseDir, rootDoc, stack));
  if (node === null || typeof node !== "object") return node;
  const ref = node["$ref"];
  if (typeof ref === "string") {
    if (stack.includes(ref)) return {}; // cycle guard -> "any" (none expected in our schemas)
    const [file, frag = ""] = ref.split("#");
    let targetDoc = rootDoc, targetBase = baseDir;
    if (file) {
      const p = resolve(baseDir, file);
      targetDoc = loadFile(p);
      targetBase = dirname(p);
    }
    const sub = frag ? pointer(targetDoc, frag) : targetDoc;
    return deref(sub, targetBase, targetDoc, [...stack, ref]);
  }
  const out = {};
  for (const [k, v] of Object.entries(node)) {
    if (k === "$id" || k === "$schema" || k === "$defs") continue; // drop identity + now-inlined defs
    out[k] = deref(v, baseDir, rootDoc, stack);
  }
  return out;
}

const [src, dst] = process.argv.slice(2);
const doc = loadFile(resolve(src));
const result = deref(doc, dirname(resolve(src)), doc, []);
result["$schema"] = "https://json-schema.org/draft/2020-12/schema";
writeFileSync(dst, JSON.stringify(result, null, 2));
console.log(`dereferenced ${src} -> ${dst}`);
