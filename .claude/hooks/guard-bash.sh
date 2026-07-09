#!/usr/bin/env bash
# PreToolUse hook on Bash: block dangerous or policy-violating commands.
# Exit code 2 tells Claude Code to deny the tool call; the message on stderr is
# surfaced as the reason.
set -euo pipefail

payload="$(cat)"
cmd="$(printf '%s' "$payload" | grep -oE '"command"[[:space:]]*:[[:space:]]*"([^"\\]|\\.)*"' | head -n1 | sed -E 's/.*"command"[[:space:]]*:[[:space:]]*"(.*)"$/\1/')"

deny() { echo "guard-bash: $1" >&2; exit 2; }

# Never bypass pre-commit hooks.
printf '%s' "$cmd" | grep -qE -- '--no-verify' && deny "pre-commit hooks are law — do not use --no-verify"

# Never force-push shared branches.
printf '%s' "$cmd" | grep -qE 'git[[:space:]]+push[[:space:]].*(--force|-f)\b' && deny "force-push is forbidden on shared branches"

# Never commit directly to develop or main (gitflow: work happens on feature/*).
if printf '%s' "$cmd" | grep -qE 'git[[:space:]]+commit'; then
  branch="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo '')"
  case "$branch" in
    develop|main) deny "direct commits to '$branch' are not allowed — branch from develop as feature/<scope>-<desc>" ;;
  esac
fi

exit 0
