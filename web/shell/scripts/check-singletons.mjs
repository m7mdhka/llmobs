// Build check: the shell must share react, react-dom, react-router-dom, and the
// design system as singletons. If any is missing or not marked singleton, a
// plugin remote could bundle its own copy — the exact failure Module Federation
// singletons exist to prevent. Run after `rspack build`; reads the MF manifest.
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const manifestPath = path.resolve(__dirname, "../dist/mf-manifest.json");

const REQUIRED = ["react", "react-dom", "react-router-dom", "@llmobs/tokens", "@llmobs/ui"];

if (!existsSync(manifestPath)) {
  console.error(`check:singletons — MF manifest not found at ${manifestPath}. Run 'rspack build' first.`);
  process.exit(1);
}

const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
const shared = manifest.shared ?? [];
const byName = new Map(shared.map((s) => [s.name, s]));

const problems = [];
for (const name of REQUIRED) {
  const entry = byName.get(name);
  if (!entry) {
    problems.push(`missing shared singleton: ${name}`);
    continue;
  }
  const isSingleton = entry.singleton === true || entry.shareConfig?.singleton === true;
  if (!isSingleton) {
    problems.push(`shared dependency not a singleton: ${name}`);
  }
}

if (problems.length > 0) {
  console.error("check:singletons FAILED:\n  - " + problems.join("\n  - "));
  process.exit(1);
}

console.log(`check:singletons OK — ${REQUIRED.length} shared singletons declared.`);
