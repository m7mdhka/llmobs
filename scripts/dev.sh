#!/usr/bin/env bash
# `make dev` — the plugin author's inner loop (J3). One command brings up:
#   - Postgres (compose, published to the host)
#   - the kernel (go run; restart = edit kernel code, Ctrl-C, rerun — a dev restart
#     is not a fault, per the H2 ruling; the supervisor re-handshakes any plugin
#     backends on restart)
#   - the web shell dev server (rspack serve :3000, HMR, proxying the API to the kernel)
#   - the tracing plugin frontend dev server (rspack serve :3001, HMR, CORS)
# The kernel's DEV_PLUGIN_REMOTES override points the registry at the plugin's live
# dev server, so editing the plugin's frontend hot-reloads in the browser with no
# rebuild and no kernel restart.
#
# Open http://localhost:3000. Ctrl-C tears everything down.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

SHELL_PORT="${SHELL_PORT:-3000}"
PLUGIN_PORT="${PLUGIN_PORT:-3001}"
KERNEL_API_PORT="${KERNEL_API_PORT:-8080}"
PG_PORT="${LLMOBS_DEV_PG_PORT:-5432}"
ADMIN_EMAIL="${LLMOBS_BOOTSTRAP_ADMIN_EMAIL:-admin@example.com}"
ADMIN_PW="${LLMOBS_BOOTSTRAP_ADMIN_PASSWORD:-admin-dev-password}"
COMPOSE=(docker compose -f deploy/compose/dev.yaml)

pids=()
cleanup() {
  echo ""
  echo ">> dev: shutting down…"
  for pid in "${pids[@]:-}"; do
    [ -n "${pid:-}" ] && kill "$pid" >/dev/null 2>&1 || true
  done
  "${COMPOSE[@]}" down >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

echo ">> dev: starting Postgres (compose)…"
LLMOBS_DEV_PG_PORT="$PG_PORT" "${COMPOSE[@]}" up -d
echo ">> dev: waiting for Postgres…"
for _ in $(seq 1 40); do
  "${COMPOSE[@]}" exec -T postgres pg_isready -U llmobs -d llmobs >/dev/null 2>&1 && break || sleep 1
done

# Build the workspace deps the dev servers resolve from dist (once). The shell + the
# plugin import @llmobs/* from their built dist, so these must exist before serve.
echo ">> dev: building shared TS packages (query-client, tokens, ui, schema-form, plugin-sdk, brand)…"
pnpm --filter @llmobs/query-client build >/dev/null
pnpm --filter @llmobs/brand build >/dev/null
pnpm --filter @llmobs/tokens build >/dev/null
pnpm --filter @llmobs/ui build >/dev/null
pnpm --filter @llmobs/schema-form build >/dev/null
pnpm --filter @llmobs/plugin-sdk build >/dev/null

echo ">> dev: starting tracing plugin dev server on :$PLUGIN_PORT (HMR)…"
PLUGIN_PORT="$PLUGIN_PORT" pnpm --filter @llmobs/plugin-tracing dev &
pids+=($!)

echo ">> dev: starting web shell dev server on :$SHELL_PORT (HMR, API -> :$KERNEL_API_PORT)…"
SHELL_PORT="$SHELL_PORT" KERNEL_URL="http://localhost:$KERNEL_API_PORT" pnpm --filter @llmobs/shell dev &
pids+=($!)

echo ">> dev: starting the kernel (go run) — plugin frontend served live from :$PLUGIN_PORT"
echo ">> dev: open http://localhost:$SHELL_PORT  (admin: $ADMIN_EMAIL / $ADMIN_PW)"
echo ">> dev: edit plugins/tracing/frontend/src/* → hot-reloads; edit kernel code → Ctrl-C + rerun"

# The kernel runs in the FOREGROUND so Ctrl-C stops it and the trap tears the rest
# down. DEV_PLUGIN_REMOTES makes the registry advertise the plugin's live dev server.
cd kernel
exec env \
  LLMOBS_DATABASE_URL="postgres://llmobs:llmobs@localhost:${PG_PORT}/llmobs?sslmode=disable" \
  LLMOBS_MIGRATE_ON_BOOT=true \
  LLMOBS_LOG_FORMAT=text \
  LLMOBS_API_ADDR=":${KERNEL_API_PORT}" \
  LLMOBS_SERVE_SHELL=false \
  LLMOBS_PLUGIN_DIR="../plugins" \
  LLMOBS_DEV_PLUGIN_REMOTES="llmobs/tracing=http://localhost:${PLUGIN_PORT}/remoteEntry.js" \
  LLMOBS_BOOTSTRAP_ADMIN_EMAIL="$ADMIN_EMAIL" \
  LLMOBS_BOOTSTRAP_ADMIN_PASSWORD="$ADMIN_PW" \
  LLMOBS_BOOTSTRAP_API_KEY="${LLMOBS_BOOTSTRAP_API_KEY:-sk-dev-demo-key}" \
  go run ./cmd/llmobsd
