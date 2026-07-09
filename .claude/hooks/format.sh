#!/usr/bin/env bash
# PostToolUse hook: run the formatter for the language of the touched file.
# Receives the tool-use payload as JSON on stdin (Claude Code hook contract).
# Best-effort: never block on formatting; exit 0 regardless.
set -euo pipefail

payload="$(cat)"
file="$(printf '%s' "$payload" | grep -oE '"file_path"[[:space:]]*:[[:space:]]*"[^"]*"' | head -n1 | sed -E 's/.*"file_path"[[:space:]]*:[[:space:]]*"([^"]*)".*/\1/')"

[ -z "${file:-}" ] && exit 0
[ -f "$file" ] || exit 0

case "$file" in
  *.go)
    command -v gofmt >/dev/null 2>&1 && gofmt -w "$file" || true
    command -v goimports >/dev/null 2>&1 && goimports -w "$file" || true
    ;;
  *.ts|*.tsx|*.js|*.jsx|*.json|*.css|*.md|*.yaml|*.yml)
    command -v pnpm >/dev/null 2>&1 && pnpm exec prettier --write "$file" >/dev/null 2>&1 || true
    ;;
esac

exit 0
