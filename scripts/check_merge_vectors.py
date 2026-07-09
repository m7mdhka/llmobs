#!/usr/bin/env python3
"""Guard: the merge test vectors in api/model/v1alpha1/05-update-semantics.md §6
are executable data (the Go conformance suite parses them). The spec is now a test
asset — fail CI if a vector JSON block drifts out of parseability or loses a
required key. This is intentionally lightweight; the Go suite runs them for real.
"""
import json
import re
import sys

SPEC = "api/model/v1alpha1/05-update-semantics.md"
REQUIRED = {"name", "entity", "events", "expect"}

text = open(SPEC).read()
blocks = re.findall(r"```json\n(.*?)\n```", text, re.S)
if len(blocks) < 16:
    print(f"FAIL: expected >= 16 JSON vector blocks, found {len(blocks)}")
    sys.exit(1)

bad = 0
for i, b in enumerate(blocks, 1):
    try:
        v = json.loads(b)
    except Exception as e:  # noqa: BLE001
        print(f"FAIL: vector block {i} is not valid JSON: {e}")
        bad += 1
        continue
    missing = REQUIRED - set(v)
    if missing:
        print(f"FAIL: vector {v.get('name', i)} missing keys: {sorted(missing)}")
        bad += 1

if bad:
    sys.exit(1)
print(f"OK: {len(blocks)} merge vectors parse and carry required keys")
