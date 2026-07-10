#!/usr/bin/env bash
# Fully-live plugin e2e (H7c): the kernel + the langfuse-compat Python backend both
# up. Proves (1) the supervisor completes a real handshake and DELIVERS the service
# token so the plugin reaches `running`, and (2) LANGFUSE_HOST->us works
# end-to-end — an unmodified Langfuse-wire ingestion batch becomes a trace visible
# through the Query API. This is the "demonstrably works" proof, not proven-in-parts.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

COMPOSE=(docker compose -f deploy/compose/lite.yaml -f deploy/compose/e2e-plugin.yaml)
KEY="sk-e2e-demo-key"
export LLMOBS_BOOTSTRAP_API_KEY="$KEY"
export LLMOBS_API_PORT="${LLMOBS_API_PORT:-18080}"
export LLMOBS_LFC_PORT="${LLMOBS_LFC_PORT:-18899}"
API="http://localhost:${LLMOBS_API_PORT}"
LFC="http://localhost:${LLMOBS_LFC_PORT}"

cleanup() { "${COMPOSE[@]}" down -v >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo ">> e2e-plugin: building + starting kernel + langfuse-compat"
"${COMPOSE[@]}" up -d --build

echo ">> e2e-plugin: waiting for kernel /readyz"
for _ in $(seq 1 60); do curl -sf ${API}/readyz >/dev/null 2>&1 && break || sleep 2; done
curl -sf ${API}/readyz >/dev/null || { echo "FAIL: kernel not ready"; "${COMPOSE[@]}" logs kernel; exit 1; }

echo ">> e2e-plugin: admin login (for the supervisor snapshot)"
CK="$(mktemp)"
curl -sf -c "$CK" -H "Content-Type: application/json" \
  -d '{"email":"admin@example.com","password":"admin-dev-password"}' ${API}/auth/login >/dev/null

echo ">> e2e-plugin: FULLY-LIVE HANDSHAKE — supervisor must bring langfuse-compat to running (token delivered)"
running=""
for _ in $(seq 1 40); do
  SNAP="$(curl -sf -b "$CK" ${API}/v1alpha1/supervisor/plugins || true)"
  STATE="$(printf '%s' "$SNAP" | python3 -c 'import sys,json;d=json.load(sys.stdin).get("plugins",[]);p=[x for x in d if x["id"]=="llmobs/langfuse-compat"];print(p[0]["state"] if p else "")' 2>/dev/null || true)"
  if [ "$STATE" = "running" ]; then running=1; break; fi
  sleep 2
done
if [ -z "$running" ]; then
  echo "FAIL: langfuse-compat did not reach running (handshake/token-delivery); last snapshot:"
  curl -sf -b "$CK" ${API}/v1alpha1/supervisor/plugins; echo; "${COMPOSE[@]}" logs kernel | tail -30; "${COMPOSE[@]}" logs langfuse-compat | tail -30
  exit 1
fi
echo "   assert OK: fully-live handshake — langfuse-compat is RUNNING (kernel delivered its service token live)"

echo ">> e2e-plugin: MIGRATION DEMO — Langfuse-wire ingestion (what an unmodified SDK POSTs) -> visible trace"
NOW="$(python3 -c 'import datetime;print(datetime.datetime.now(datetime.UTC).strftime("%Y-%m-%dT%H:%M:%S.000Z"))')"
BATCH="$(python3 - "$NOW" <<'PY'
import json,sys
now=sys.argv[1]
print(json.dumps({"batch":[
  {"id":"e1","type":"trace-create","body":{"id":"trace-e2e","name":"chat","userId":"u-1","timestamp":now}},
  {"id":"e2","type":"generation-create","body":{"id":"gen-e2e","traceId":"trace-e2e","name":"llm","model":"gpt-4o","usage":{"input":10,"output":5,"unit":"TOKENS"},"startTime":now,"endTime":now}},
]}))
PY
)"
ING="$(curl -s -o /dev/null -w '%{http_code}' -H "Content-Type: application/json" -d "$BATCH" ${LFC}/api/public/ingestion)"
[ "$ING" = "200" ] || { echo "FAIL: langfuse ingestion returned $ING"; "${COMPOSE[@]}" logs langfuse-compat | tail -30; exit 1; }

# The plugin derives the OTLP trace_id by hashing the Langfuse id (documented lossy edge).
TID="$(python3 -c 'import hashlib;print(hashlib.sha256(b"trace-e2e").hexdigest()[:32])')"
FROM="$(python3 -c 'import datetime;print((datetime.datetime.now(datetime.UTC)-datetime.timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ"))')"
TO="$(python3 -c 'import datetime;print((datetime.datetime.now(datetime.UTC)+datetime.timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ"))')"
Q="{\"target\":\"spans\",\"timeRange\":{\"from\":\"$FROM\",\"to\":\"$TO\"},\"filters\":[{\"field\":\"trace_id\",\"op\":\"eq\",\"value\":\"$TID\"}]}"

echo ">> e2e-plugin: querying the migrated trace back through the Query API"
COUNT=0
for _ in $(seq 1 30); do
  RESP="$(curl -sf -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" -d "$Q" ${API}/v1alpha1/query || true)"
  COUNT="$(printf '%s' "$RESP" | python3 -c 'import sys,json;print(len(json.load(sys.stdin).get("data",[])))' 2>/dev/null || echo 0)"
  [ "$COUNT" -ge 2 ] && break || sleep 1
done
[ "$COUNT" -ge 2 ] || { echo "FAIL: migrated trace not visible (got $COUNT spans)"; echo "$RESP"; "${COMPOSE[@]}" logs langfuse-compat | tail -30; exit 1; }
# The spans are kernel-stamped with the plugin as source (cold-path ingest, H7a).
printf '%s' "$RESP" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
srcs={s.get('attributes',{}).get('llmobs.source') for s in d}
assert 'plugin:llmobs/langfuse-compat' in srcs, srcs
print('   assert OK: %d spans migrated via LANGFUSE_HOST->us, source-stamped %s'%(len(d), 'plugin:llmobs/langfuse-compat'))
" || { echo "FAIL: source stamp"; echo "$RESP"; exit 1; }
rm -f "$CK"

echo "E2E_PLUGIN_OK"
