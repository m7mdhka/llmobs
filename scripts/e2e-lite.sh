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
export LLMOBS_METRICS_PORT="${LLMOBS_METRICS_PORT:-19090}"
API="http://localhost:${LLMOBS_API_PORT}"
METRICS="http://localhost:${LLMOBS_METRICS_PORT}"

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
# The registry endpoint the shell's loader consumes advertises the baked-in
# tracing plugin, and its MF remoteEntry is served with an SRI integrity hash.
REG="$(curl -sf ${API}/v1alpha1/registry/plugins || true)"
ENTRY="$(printf '%s' "$REG" | python3 -c '
import sys,json
d=json.load(sys.stdin)["plugins"]
tr=[p for p in d if p["id"]=="llmobs/tracing"]
assert tr, "tracing plugin not advertised by registry"
p=tr[0]
assert p["integrity"].startswith("sha384-"), p.get("integrity")
assert any(n["path"]=="/traces" for n in p["nav"]), "missing /traces nav"
print(p["remoteEntry"])
')" || { echo "FAIL: registry did not advertise the tracing plugin"; echo "$REG"; exit 1; }
RE_STATUS="$(curl -s -o /dev/null -w '%{http_code}' ${API}${ENTRY})"
[ "$RE_STATUS" = "200" ] || { echo "FAIL: plugin remoteEntry $ENTRY returned $RE_STATUS"; exit 1; }
echo "   assert OK: shell HTML at /, SPA fallback, tracing plugin advertised + remoteEntry served"

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

echo ">> e2e-lite: /metrics exposition (separate bind) + trace activity fields"
MOUT="$(curl -sf ${METRICS}/metrics || true)"
printf '%s' "$MOUT" | grep -q 'llmobs_ingest_spans_total{' \
  || { echo "FAIL: /metrics missing ingest counter"; printf '%s\n' "$MOUT" | head; exit 1; }
printf '%s' "$MOUT" | grep -q '# TYPE llmobs_pipeline_stage_seconds histogram' \
  || { echo "FAIL: /metrics missing stage histogram"; exit 1; }
printf '%s' "$MOUT" | grep -q 'llmobs_db_pool_total_conns' \
  || { echo "FAIL: /metrics missing pool gauge"; exit 1; }
echo "   assert OK: /metrics exposes ingest + stage + pool series"

echo ">> e2e-lite: querying the traces DSL target (derived from spans)"
TQUERY="{\"target\":\"traces\",\"timeRange\":{\"from\":\"$FROM\",\"to\":\"$TO\"},\"filters\":[{\"field\":\"environment\",\"op\":\"eq\",\"value\":\"production\"}],\"limit\":10}"
TRESP="$(curl -sf -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" -d "$TQUERY" ${API}/v1alpha1/query || true)"
printf '%s' "$TRESP" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
assert len(d)>=1, f'expected >=1 trace, got {len(d)}'
tr=[t for t in d if t['id']=='$TRACE_ID']
assert tr, 'emitted trace not found in traces target'
t=tr[0]
assert t.get('environment')=='production', t.get('environment')
assert t.get('span_count',0)>=4, t.get('span_count')
assert t.get('start_time'), 'trace start_time not synthesized'
assert 'is_open' in t, 'trace is_open not synthesized'
assert t.get('last_activity'), 'trace last_activity not synthesized'
assert isinstance(t.get('status'),dict), 'trace status shape'
print('   assert OK: traces target returned synthesized trace, span_count=%d, env=%s'%(t['span_count'],t['environment']))
" || { echo "FAIL: traces target"; echo "$TRESP"; exit 1; }

echo ">> e2e-lite: QD-4 aggregation (count by kind — golden result)"
AGGQ="{\"target\":\"spans\",\"timeRange\":{\"from\":\"$FROM\",\"to\":\"$TO\"},\"filters\":[{\"field\":\"trace_id\",\"op\":\"eq\",\"value\":\"$TRACE_ID\"}],\"groupBy\":[\"kind\"],\"aggregations\":[{\"op\":\"count\"}]}"
AGGRESP="$(curl -sf -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" -d "$AGGQ" ${API}/v1alpha1/query || true)"
printf '%s' "$AGGRESP" | python3 -c "
import sys,json
rows=json.load(sys.stdin)['data']
by={r['g0']:r['count'] for r in rows}
# golden: the seeded agent trace has exactly these four kinds, one span each
assert by=={'generation':1,'agent_step':1,'tool_call':1,'span':1}, by
print('   assert OK: count-by-kind golden = %s'%by)
" || { echo "FAIL: aggregation golden"; echo "$AGGRESP"; exit 1; }

echo ">> e2e-lite: payload-scope enforcement (metadata key never receives payloads)"
PCK="$(mktemp)"
PLGN="$(curl -sf -c "$PCK" -H "Content-Type: application/json" \
  -d '{"email":"admin@example.com","password":"admin-dev-password"}' ${API}/auth/login)"
PXC="$(printf '%s' "$PLGN" | python3 -c 'import sys,json;print(json.load(sys.stdin)["csrf_token"])')"
METAKEY="$(curl -sf -b "$PCK" -X POST -H "X-CSRF-Token: $PXC" -H "Content-Type: application/json" \
  -d '{"scopes":["query"]}' ${API}/v1alpha1/api-keys | python3 -c 'import sys,json;print(json.load(sys.stdin)["secret"])')"
PAYKEY="$(curl -sf -b "$PCK" -X POST -H "X-CSRF-Token: $PXC" -H "Content-Type: application/json" \
  -d '{"scopes":["query","query:payloads"]}' ${API}/v1alpha1/api-keys | python3 -c 'import sys,json;print(json.load(sys.stdin)["secret"])')"
SPANQ="{\"target\":\"spans\",\"timeRange\":{\"from\":\"$FROM\",\"to\":\"$TO\"},\"filters\":[{\"field\":\"trace_id\",\"op\":\"eq\",\"value\":\"$TRACE_ID\"}]}"
# metadata-scoped: NO span may carry attributes/input/output/events/model_parameters.
curl -sf -H "Authorization: Bearer $METAKEY" -H "Content-Type: application/json" -d "$SPANQ" ${API}/v1alpha1/query \
  | python3 -c "
import sys,json
for s in json.load(sys.stdin)['data']:
    for f in ('attributes','input','output','events','model_parameters'):
        assert f not in s, 'metadata scope leaked '+f
print('   metadata scope: no payload fields present')
" || { echo "FAIL: metadata scope leaked payloads"; exit 1; }
# payloads-scoped: attributes present, AND redaction-before-persist is proven —
# the stored input carries [REDACTED:*] tokens, never the raw PII, plus the dq signal.
curl -sf -H "Authorization: Bearer $PAYKEY" -H "Content-Type: application/json" -d "$SPANQ" ${API}/v1alpha1/query \
  | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
assert any('attributes' in s for s in d), 'payloads scope should include attributes'
gen=[s for s in d if s.get('kind')=='generation']
assert gen, 'no generation span'
raw=json.dumps(gen[0])   # grep the persisted JSON, structurally
assert 'jane@example.com' not in raw, 'raw email survived redaction!'
assert '4111 1111 1111 1111' not in raw, 'raw card survived redaction!'
assert '[REDACTED:email]' in raw and '[REDACTED:credit_card]' in raw, 'redaction tokens missing'
assert gen[0].get('attributes',{}).get('llmobs.dq.redacted',{}).get('total',0) >= 2, 'dq.redacted signal missing'
print('   redaction-before-persist: PII scrubbed to tokens, dq.redacted stamped')
" || { echo "FAIL: redaction-before-persist"; exit 1; }
echo ">> e2e-lite: MCP server (metadata-safe, JSON-RPC over stdio)"
MCPBIN="$(mktemp -u)"
go build -o "$MCPBIN" ./tools/mcp-server || { echo "FAIL: mcp build"; exit 1; }
# Safety posture: a payload-scoped key is refused without --allow-payloads.
if LLMOBS_URL="$API" LLMOBS_API_KEY="$PAYKEY" "$MCPBIN" </dev/null >/dev/null 2>/tmp/mcp_refuse.txt; then
  echo "FAIL: MCP server should refuse a payload key"; exit 1
fi
grep -qi 'refusing to start' /tmp/mcp_refuse.txt || { echo "FAIL: wrong refusal"; cat /tmp/mcp_refuse.txt; exit 1; }
# Drive a JSON-RPC session with the metadata key: initialize -> tools/list -> get_trace_tree.
MCPOUT="$(printf '%s\n%s\n%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"get_trace_tree\",\"arguments\":{\"trace_id\":\"$TRACE_ID\"}}}" \
  | LLMOBS_URL="$API" LLMOBS_API_KEY="$METAKEY" "$MCPBIN" 2>/dev/null)"
printf '%s' "$MCPOUT" | python3 -c "
import sys,json
resps={}
for line in sys.stdin:
    line=line.strip()
    if not line: continue
    r=json.loads(line); resps[r.get('id')]=r
assert len(resps[2]['result']['tools'])==5, 'expected 5 tools'
names={t['name'] for t in resps[2]['result']['tools']}
assert 'query_traces' in names and 'top_costs' in names, names
tree=json.loads(resps[3]['result']['content'][0]['text'])
assert tree['span_count']>=4, tree
# metadata-safe: no payload keys in the summarized spans
for sp in tree['spans']:
    for f in ('input','output','events','attributes'):
        assert f not in sp, 'MCP leaked '+f
print('   assert OK: 5 tools, tree via MCP (%d spans), no payloads in summaries'%tree['span_count'])
" || { echo "FAIL: MCP session"; echo "$MCPOUT"; exit 1; }
rm -f "$MCPBIN"
rm -f "$PCK"
echo "   assert OK: payload-scope enforced on the query path + MCP metadata-safe"

echo ">> e2e-lite: machine API-key issuance + score write + scores target + QD-9 semi-join"
NOW="$(python3 -c 'import datetime;print(datetime.datetime.now(datetime.UTC).strftime("%Y-%m-%dT%H:%M:%SZ"))')"
CK="$(mktemp)"
LGN="$(curl -sf -c "$CK" -H "Content-Type: application/json" \
  -d '{"email":"admin@example.com","password":"admin-dev-password"}' ${API}/auth/login)"
XCSRF="$(printf '%s' "$LGN" | python3 -c 'import sys,json;print(json.load(sys.stdin)["csrf_token"])')"
# Issue a scoped machine key (admin session + CSRF); secret shown once.
KEYRESP="$(curl -sf -b "$CK" -X POST -H "X-CSRF-Token: $XCSRF" -H "Content-Type: application/json" \
  -d '{"scopes":["scores:write","query"]}' ${API}/v1alpha1/api-keys)"
SCKEY="$(printf '%s' "$KEYRESP" | python3 -c 'import sys,json;print(json.load(sys.stdin)["secret"])')"
SCPK="$(printf '%s' "$KEYRESP" | python3 -c 'import sys,json;print(json.load(sys.stdin)["public_key"])')"
[ -n "$SCKEY" ] || { echo "FAIL: key issuance"; echo "$KEYRESP"; exit 1; }
# Write a score against the trace, via the issued key (not the bootstrap key).
SCORE="{\"id\":\"score-e2e-1\",\"subject_type\":\"trace\",\"subject_id\":\"$TRACE_ID\",\"name\":\"hallucination\",\"data_type\":\"numeric\",\"value_numeric\":0.2,\"source\":\"eval\",\"timestamp\":\"$NOW\",\"environment\":\"production\"}"
WR="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $SCKEY" -H "Content-Type: application/json" -d "$SCORE" ${API}/v1alpha1/scores)"
[ "$WR" = "201" ] || { echo "FAIL: score write returned $WR"; exit 1; }
# A wrong-typed value is rejected 422 (no coercion, LM-3).
BADSCORE="{\"id\":\"bad\",\"subject_type\":\"trace\",\"subject_id\":\"$TRACE_ID\",\"name\":\"x\",\"data_type\":\"numeric\",\"value_numeric\":\"0.2\",\"source\":\"eval\",\"timestamp\":\"$NOW\",\"environment\":\"production\"}"
BADWR="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $SCKEY" -H "Content-Type: application/json" -d "$BADSCORE" ${API}/v1alpha1/scores)"
[ "$BADWR" = "422" ] || { echo "FAIL: wrong-typed score returned $BADWR (want 422)"; exit 1; }
# Scores target returns the written score.
SCQ="{\"target\":\"scores\",\"timeRange\":{\"from\":\"$FROM\",\"to\":\"$TO\"},\"filters\":[{\"field\":\"name\",\"op\":\"eq\",\"value\":\"hallucination\"}]}"
SCRESP="$(curl -sf -H "Authorization: Bearer $SCKEY" -H "Content-Type: application/json" -d "$SCQ" ${API}/v1alpha1/query || true)"
printf '%s' "$SCRESP" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
assert any(s['id']=='score-e2e-1' and s['subject_id']=='$TRACE_ID' for s in d), 'score not found via scores target'
" || { echo "FAIL: scores target"; echo "$SCRESP"; exit 1; }
# QD-9 semi-join: traces having a score hallucination < 0.5 includes our trace.
QD9="{\"target\":\"traces\",\"timeRange\":{\"from\":\"$FROM\",\"to\":\"$TO\"},\"scores\":[{\"name\":\"hallucination\",\"data_type\":\"numeric\",\"op\":\"lt\",\"value\":0.5}]}"
QDRESP="$(curl -sf -H "Authorization: Bearer $SCKEY" -H "Content-Type: application/json" -d "$QD9" ${API}/v1alpha1/query || true)"
printf '%s' "$QDRESP" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
assert any(t['id']=='$TRACE_ID' for t in d), 'QD-9 semi-join did not select the scored trace'
" || { echo "FAIL: QD-9 semi-join"; echo "$QDRESP"; exit 1; }
# A tighter threshold excludes it (score 0.2 is NOT < 0.1).
QD9B="{\"target\":\"traces\",\"timeRange\":{\"from\":\"$FROM\",\"to\":\"$TO\"},\"scores\":[{\"name\":\"hallucination\",\"data_type\":\"numeric\",\"op\":\"lt\",\"value\":0.1}]}"
QDB="$(curl -sf -H "Authorization: Bearer $SCKEY" -H "Content-Type: application/json" -d "$QD9B" ${API}/v1alpha1/query || true)"
printf '%s' "$QDB" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
assert not any(t['id']=='$TRACE_ID' for t in d), 'QD-9 semi-join wrongly selected the trace at a tighter threshold'
" || { echo "FAIL: QD-9 negative case"; echo "$QDB"; exit 1; }
# Revoke the key; a subsequent write is rejected.
curl -sf -b "$CK" -X DELETE -H "X-CSRF-Token: $XCSRF" ${API}/v1alpha1/api-keys/${SCPK} >/dev/null || { echo "FAIL: revoke"; exit 1; }
REVWR="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $SCKEY" -H "Content-Type: application/json" -d "$SCORE" ${API}/v1alpha1/scores)"
[ "$REVWR" = "403" ] || { echo "FAIL: revoked key still works ($REVWR)"; exit 1; }
# GDPR erasure: delete the user-42 span, provably (audit id + count).
ERASE="$(curl -sf -b "$CK" -X DELETE "${API}/v1alpha1/spans?user_id=user-42&from=${FROM}&to=${TO}" || true)"
printf '%s' "$ERASE" | python3 -c "
import sys,json
d=json.load(sys.stdin)
assert d.get('erased',0) >= 1, 'expected >=1 span erased'
assert d.get('audit_id','').startswith('era_'), 'missing erasure audit id'
print('   erased %d span(s), audit=%s'%(d['erased'], d['audit_id']))
" || { echo "FAIL: erasure"; echo "$ERASE"; exit 1; }
# The erased user's spans are gone.
LEFT="$(curl -sf -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d "{\"target\":\"spans\",\"timeRange\":{\"from\":\"$FROM\",\"to\":\"$TO\"},\"filters\":[{\"field\":\"user_id\",\"op\":\"eq\",\"value\":\"user-42\"}]}" \
  ${API}/v1alpha1/query | python3 -c 'import sys,json;print(len(json.load(sys.stdin).get("data",[])))')"
[ "$LEFT" = "0" ] || { echo "FAIL: erased user still has $LEFT spans"; exit 1; }
rm -f "$CK"
echo "   assert OK: key issued, score written (201) + bad-score 422, scores target, QD-9 +/- , revocation 403, GDPR erasure + audit"

echo "E2E_LITE_OK"
