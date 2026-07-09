#!/usr/bin/env bash
# Regenerate all code from the api/ contracts. Idempotent: same contracts +
# pinned generators => byte-identical output. See tools/codegen/README.md.
#
# Generators (pinned): quicktype (tools/codegen/package.json) for JSON Schema ->
# Go/TS types; oapi-codegen (version below, via `go run`) for OpenAPI -> Go server.
set -euo pipefail

OAPI_CODEGEN_VERSION="v2.4.1"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CG="$ROOT/tools/codegen"
QT="$CG/node_modules/.bin/quicktype"
MODEL_SCHEMA="$ROOT/api/model/v1alpha1/schema"
DSL_SCHEMA="$ROOT/api/query/v1alpha1/dsl.schema.json"
OPENAPI="$ROOT/api/openapi/v1alpha1/query.yaml"

GO_MODEL_OUT="$ROOT/kernel/pkg/model/model_gen.go"
GO_SERVER_OUT="$ROOT/kernel/internal/gateway/queryapi/server_gen.go"
TS_TYPES_OUT="$ROOT/packages/query-client/src/types.gen.ts"
BUNDLE="$CG/.openapi-bundle.json"   # build artifact (git-ignored)

if [ ! -x "$QT" ]; then
  echo "error: quicktype not installed. Run 'pnpm --dir tools/codegen install' (or 'make setup')." >&2
  exit 1
fi

# Dereference schemas into self-contained documents (inline $ref, strip $id) so
# quicktype generation is deterministic across environments (its cross-file/$id
# resolution is node-version-sensitive). Deref output is a git-ignored artifact.
DEREF="$CG/.deref"
rm -rf "$DEREF"; mkdir -p "$DEREF"
for s in span trace score score-config media-reference; do
  node "$CG/deref_schema.mjs" "$MODEL_SCHEMA/$s.schema.json" "$DEREF/$s.schema.json"
done
node "$CG/deref_schema.mjs" "$DSL_SCHEMA" "$DEREF/dsl.schema.json"

echo ">> codegen: Go model types (JSON Schema -> kernel/pkg/model)"
mkdir -p "$(dirname "$GO_MODEL_OUT")"
"$QT" -s schema --lang go --package model \
  "$DEREF/span.schema.json" \
  "$DEREF/trace.schema.json" \
  "$DEREF/score.schema.json" \
  "$DEREF/score-config.schema.json" \
  "$DEREF/media-reference.schema.json" \
  -o "$GO_MODEL_OUT"

echo ">> codegen: TypeScript types (JSON Schema + DSL -> packages/query-client)"
mkdir -p "$(dirname "$TS_TYPES_OUT")"
"$QT" -s schema --lang ts --just-types --nice-property-names \
  "$DEREF/span.schema.json" \
  "$DEREF/trace.schema.json" \
  "$DEREF/score.schema.json" \
  "$DEREF/score-config.schema.json" \
  "$DEREF/media-reference.schema.json" \
  "$DEREF/dsl.schema.json" \
  -o "$TS_TYPES_OUT"

echo ">> codegen: Go server interface (OpenAPI -> kernel gateway)"
mkdir -p "$(dirname "$GO_SERVER_OUT")"
node "$CG/bundle_openapi.mjs" "$OPENAPI" "$BUNDLE"
go run "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@${OAPI_CODEGEN_VERSION}" \
  -generate types,std-http-server -package queryapi -o "$GO_SERVER_OUT" "$BUNDLE"

echo ">> codegen: done"
