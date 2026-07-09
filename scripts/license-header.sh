#!/usr/bin/env bash
# Ensure every Go/TS source file carries the Apache-2.0 license header.
# Used by the pre-commit `license-header` hook. Receives file paths as args.
# Inserts the header when missing; exits non-zero if it had to modify files so
# the commit is re-staged intentionally.
set -euo pipefail

YEAR="2026"
HOLDER="m7mdhka"
MARKER="Licensed under the Apache License, Version 2.0"

header_go() {
  cat <<EOF
// Copyright ${YEAR} ${HOLDER}
//
// ${MARKER} (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

EOF
}

header_ts() {
  cat <<EOF
/*
 * Copyright ${YEAR} ${HOLDER}
 *
 * ${MARKER} (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

EOF
}

changed=0
for f in "$@"; do
  [ -f "$f" ] || continue
  # Skip generated files.
  grep -qiE 'code generated|do not edit|@generated' "$f" && continue
  grep -qF "$MARKER" "$f" && continue

  case "$f" in
    *.go)          tmp="$(mktemp)"; header_go >"$tmp"; cat "$f" >>"$tmp"; mv "$tmp" "$f"; changed=1 ;;
    *.ts|*.tsx)    tmp="$(mktemp)"; header_ts >"$tmp"; cat "$f" >>"$tmp"; mv "$tmp" "$f"; changed=1 ;;
  esac
  [ "$changed" = 1 ] && echo "inserted license header: $f"
done

exit "$changed"
