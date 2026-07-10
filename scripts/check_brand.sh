#!/usr/bin/env bash
# Brand guard (D15, web mirror of the kernel-side rule): the user-visible product
# display name must appear ONLY in packages/brand. Every other web surface reads
# it from @llmobs/brand, so white-labeling is a deployment concern, not a fork.
#
# Matches the standalone display name "LLMObs" — not code identifiers
# (useLLMObs, LLMObsPluginProvider) or header names (X-LLMObs-*), which are API
# symbols, not branding.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# Word-boundary match: not preceded by an identifier char or '-', not followed by
# an identifier char. Catches >LLMObs<, "LLMObs", `Starting LLMObs…`; skips
# useLLMObs, LLMObsPlugin, X-LLMObs.
pattern='(^|[^-A-Za-z_])LLMObs([^A-Za-z_]|$)'

scan_dirs=(web/shell/src)
for d in plugins/*/frontend/src templates/*/frontend/src; do
  [ -d "$d" ] && scan_dirs+=("$d")
done

hits="$(grep -rnE "$pattern" "${scan_dirs[@]}" 2>/dev/null || true)"
if [ -n "$hits" ]; then
  echo "brand-guard FAILED: the display name must come from @llmobs/brand, not be hardcoded:" >&2
  echo "$hits" >&2
  exit 1
fi
echo "brand-guard OK — no hardcoded display name outside @llmobs/brand."
