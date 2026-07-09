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
