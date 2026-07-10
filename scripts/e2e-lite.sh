#!/usr/bin/env bash
# End-to-end lite-profile loop (bootstrap-order milestone): bring up the lite
# stack, emit one agent-shaped OTel trace, and read its spans back through the
# Query DSL. Asserts the full path OTLP -> pipeline -> Postgres -> query.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

COMPOSE=(docker compose -f deploy/compose/lite.yaml)
KEY="sk-e2e-demo-key"
export LLMOBS_BOOTSTRAP_API_KEY="$KEY"
# Overridable host ports (defaults dodge common local conflicts; CI can override).
export LLMOBS_API_PORT="${LLMOBS_API_PORT:-18080}"
export LLMOBS_OTLP_PORT="${LLMOBS_OTLP_PORT:-14318}"
export LLMOBS_OTLP_GRPC_PORT="${LLMOBS_OTLP_GRPC_PORT:-14317}"
API="http://localhost:${LLMOBS_API_PORT}"

cleanup() { "${COMPOSE[@]}" down -v >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo ">> e2e-lite: bringing up the lite stack (build)"
"${COMPOSE[@]}" up -d --build

echo ">> e2e-lite: waiting for kernel /readyz"
ready=""
for _ in $(seq 1 60); do
  if curl -sf ${API}/readyz >/dev/null 2>&1; then ready=1; break; fi
  sleep 2
done
if [ -z "$ready" ]; then echo "FAIL: kernel not ready"; "${COMPOSE[@]}" logs kernel; exit 1; fi

echo ">> e2e-lite: web shell served at the origin root"
ROOT_HTML="$(curl -sf ${API}/ || true)"
printf '%s' "$ROOT_HTML" | grep -q '<div id="root">' \
  || { echo "FAIL: shell index.html not served at /"; printf '%s\n' "$ROOT_HTML" | head -5; exit 1; }
# SPA fallback: an unknown client route returns the app HTML, not a 404.
DEEP_STATUS="$(curl -s -o /dev/null -w '%{http_code}' ${API}/traces)"
[ "$DEEP_STATUS" = "200" ] || { echo "FAIL: SPA deep-link /traces returned $DEEP_STATUS"; exit 1; }
# The registry endpoint the shell's loader consumes exists and is empty in D2.
REG="$(curl -sf ${API}/v1alpha1/registry/plugins || true)"
printf '%s' "$REG" | python3 -c 'import sys,json;d=json.load(sys.stdin);assert d.get("plugins")==[]' \
  || { echo "FAIL: registry endpoint not empty-list"; echo "$REG"; exit 1; }
echo "   assert OK: shell HTML at /, SPA fallback, empty registry"

echo ">> e2e-lite: auth smoke (login -> me -> logout)"
COOKIES="$(mktemp)"
LOGIN="$(curl -sf -c "$COOKIES" -H "Content-Type: application/json" \
  -d '{"email":"admin@example.com","password":"admin-dev-password"}' ${API}/auth/login || true)"
CSRF="$(printf '%s' "$LOGIN" | python3 -c 'import sys,json;print(json.load(sys.stdin)["csrf_token"])' 2>/dev/null || echo '')"
if [ -z "$CSRF" ]; then echo "FAIL: login did not return a csrf token"; echo "$LOGIN"; exit 1; fi
ME="$(curl -sf -b "$COOKIES" ${API}/auth/me || true)"
printf '%s' "$ME" | python3 -c 'import sys,json;assert json.load(sys.stdin)["user"]["email"]=="admin@example.com"' \
  || { echo "FAIL: /auth/me did not return the admin"; echo "$ME"; exit 1; }
# A session-authenticated query must work without any bearer key.
SQ_STATUS="$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIES" -H "Content-Type: application/json" \
  -d '{"target":"spans","timeRange":{"from":"2020-01-01T00:00:00Z","to":"2020-01-02T00:00:00Z"}}' ${API}/v1alpha1/query)"
[ "$SQ_STATUS" = "200" ] || { echo "FAIL: session-auth query returned $SQ_STATUS"; exit 1; }
# Bad login is rejected.
BAD_STATUS="$(curl -s -o /dev/null -w '%{http_code}' -H "Content-Type: application/json" \
  -d '{"email":"admin@example.com","password":"wrong"}' ${API}/auth/login)"
[ "$BAD_STATUS" = "401" ] || { echo "FAIL: bad login returned $BAD_STATUS (want 401)"; exit 1; }
curl -sf -b "$COOKIES" -X POST -H "X-CSRF-Token: $CSRF" ${API}/auth/logout >/dev/null || { echo "FAIL: logout"; exit 1; }
rm -f "$COOKIES"
echo "   assert OK: login/me/session-query/bad-login/logout"

echo ">> e2e-lite: emitting an agent-shaped OTel trace"
TRACE_ID="$(LLMOBS_API_KEY="$KEY" LLMOBS_OTLP_ENDPOINT=localhost:${LLMOBS_OTLP_PORT} go run ./examples/otel-genai-demo)"
echo "   trace_id=$TRACE_ID"

FROM="$(python3 -c 'import datetime;print((datetime.datetime.now(datetime.UTC)-datetime.timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ"))')"
TO="$(python3 -c 'import datetime;print((datetime.datetime.now(datetime.UTC)+datetime.timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ"))')"
QUERY="{\"version\":\"v1alpha1\",\"target\":\"spans\",\"timeRange\":{\"from\":\"$FROM\",\"to\":\"$TO\"},\"filters\":[{\"field\":\"trace_id\",\"op\":\"eq\",\"value\":\"$TRACE_ID\"}],\"limit\":50}"

echo ">> e2e-lite: querying spans via the DSL (polling for async persistence)"
RESP=""; COUNT=0
for _ in $(seq 1 30); do
  RESP="$(curl -sf -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
    -d "$QUERY" ${API}/v1alpha1/query || true)"
  COUNT="$(printf '%s' "$RESP" | python3 -c 'import sys,json;print(len(json.load(sys.stdin).get("data",[])))' 2>/dev/null || echo 0)"
  if [ "$COUNT" -ge 4 ]; then break; fi
  sleep 1
done
echo "   spans returned: $COUNT"
if [ "$COUNT" -lt 4 ]; then echo "FAIL: expected >= 4 spans"; echo "$RESP"; "${COMPOSE[@]}" logs kernel | tail -30; exit 1; fi

echo ">> e2e-lite: asserting the canonical mapping came back"
printf '%s' "$RESP" | python3 -c '
import sys,json
d=json.load(sys.stdin)["data"]
kinds={s["kind"] for s in d}
assert "generation" in kinds and "agent_step" in kinds and "tool_call" in kinds, f"kinds={kinds}"
gen=[s for s in d if s["kind"]=="generation"][0]
assert gen.get("model")=="gpt-4o-2024-08-06", gen.get("model")
assert gen.get("provider")=="openai", gen.get("provider")
assert gen.get("environment")=="production", gen.get("environment")
assert gen.get("provided_usage_details",{}).get("input")==812
print("   assert OK: kinds="+",".join(sorted(kinds))+", generation model/usage/env verified")
'

echo ">> e2e-lite: fetching the trace tree"
TREE="$(curl -sf -H "Authorization: Bearer $KEY" ${API}/v1alpha1/traces/${TRACE_ID}/tree || true)"
printf '%s' "$TREE" | python3 -c '
import sys,json
t=json.load(sys.stdin)
trace=t["trace"]; spans=t["spans"]
assert trace["id"], "trace.id missing"
assert len(spans)>=4, f"expected >=4 spans in tree, got {len(spans)}"
# tree order: every non-root parent appears before its children
pos={s["id"]:i for i,s in enumerate(spans)}
for i,s in enumerate(spans):
    p=s.get("parent_span_id") or ""
    if p and p in pos:
        assert pos[p] < i, "child precedes parent: "+s["id"]
assert trace.get("start_time"), "trace.start_time not synthesized"
print(f"   assert OK: tree of {len(spans)} spans in parent-before-child order, trace synthesized")
'

echo "E2E_LITE_OK"
