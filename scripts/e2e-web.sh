#!/usr/bin/env bash
# Browser e2e for the platform slice: bring up the lite stack (kernel serving the
# shell + the tracing plugin baked in), seed a trace, then drive Chromium through
# login → manifest nav → traces list → span tree → span panel. A gate, not a suite.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

COMPOSE=(docker compose --env-file deploy/compose/dev.env -f deploy/compose/lite.yaml)
KEY="sk-e2e-demo-key"
export LLMOBS_BOOTSTRAP_API_KEY="$KEY"
export LLMOBS_API_PORT="${LLMOBS_API_PORT:-18080}"
export LLMOBS_OTLP_PORT="${LLMOBS_OTLP_PORT:-14318}"
export LLMOBS_OTLP_GRPC_PORT="${LLMOBS_OTLP_GRPC_PORT:-14317}"
API="http://localhost:${LLMOBS_API_PORT}"

cleanup() { "${COMPOSE[@]}" down -v >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo ">> e2e-web: bringing up the lite stack (build)"
"${COMPOSE[@]}" up -d --build

echo ">> e2e-web: waiting for kernel /readyz"
for _ in $(seq 1 60); do curl -sf ${API}/readyz >/dev/null 2>&1 && break; sleep 2; done

echo ">> e2e-web: seeding an agent-shaped trace"
LLMOBS_API_KEY="$KEY" LLMOBS_OTLP_ENDPOINT=localhost:${LLMOBS_OTLP_PORT} go run ./examples/otel-genai-demo >/dev/null
# Give the async pipeline a moment to persist.
for _ in $(seq 1 20); do
  n="$(curl -sf -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
    -d "{\"target\":\"traces\",\"timeRange\":{\"from\":\"2020-01-01T00:00:00Z\",\"to\":\"2030-01-01T00:00:00Z\"},\"limit\":1}" \
    ${API}/v1alpha1/query | python3 -c 'import sys,json;print(len(json.load(sys.stdin).get("data",[])))' 2>/dev/null || echo 0)"
  [ "$n" -ge 1 ] && break
  sleep 1
done

echo ">> e2e-web: installing Playwright chromium (first run only)"
pnpm --filter @llmobs/e2e-web install >/dev/null 2>&1 || pnpm install --filter @llmobs/e2e-web >/dev/null 2>&1 || true
pnpm --filter @llmobs/e2e-web exec playwright install --with-deps chromium >/dev/null 2>&1 \
  || pnpm --filter @llmobs/e2e-web exec playwright install chromium

echo ">> e2e-web: running the browser smoke"
pnpm --filter @llmobs/e2e-web exec playwright test

echo "E2E_WEB_OK"
