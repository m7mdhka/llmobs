#!/usr/bin/env python3
"""Validate the api/ JSON Schemas and their examples.

- Meta-validates every model + query schema (Draft 2020-12).
- Model examples: each NN.valid.json MUST pass; each NN.invalid.json MUST fail.
- Query DSL examples: each NN-*.json MUST pass dsl.schema.json; each invalid-*.json
  MUST fail.

Exit non-zero on any failure. Used locally and in CI (contracts job).
Requires: jsonschema, referencing.
"""
import glob
import json
import os
import sys

from jsonschema import Draft202012Validator
from referencing import Registry, Resource

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODEL = os.path.join(ROOT, "api/model/v1alpha1/schema")
QUERY = os.path.join(ROOT, "api/query/v1alpha1")

failures = []


def load(p):
    with open(p) as f:
        return json.load(f)


def registry_for(dir_):
    res = []
    for f in glob.glob(os.path.join(dir_, "*.schema.json")):
        s = load(f)
        r = Resource.from_contents(s)
        res += [(s["$id"], r), (os.path.basename(f), r)]
    return Registry().with_resources(res)


def check(tag, schema, reg, instance, should_pass):
    errs = sorted(e.message for e in Draft202012Validator(schema, registry=reg).iter_errors(instance))
    ok = (not errs) if should_pass else bool(errs)
    status = "ok " if ok else "FAIL"
    print(f"  [{status}] {tag}" + ("" if ok else f"  ({'unexpectedly rejected: ' + str(errs[:1]) if should_pass else 'unexpectedly accepted'})"))
    if not ok:
        failures.append(tag)


print("== model schemas ==")
mreg = registry_for(MODEL)
for f in sorted(glob.glob(os.path.join(MODEL, "*.schema.json"))):
    try:
        Draft202012Validator.check_schema(load(f))
    except Exception as e:  # noqa: BLE001
        print(f"  [FAIL] meta {os.path.basename(f)}: {e}")
        failures.append(f)
for base in ["span", "trace", "score", "score-config", "media-reference"]:
    schema = load(os.path.join(MODEL, f"{base}.schema.json"))
    check(f"{base}.valid", schema, mreg, load(os.path.join(MODEL, f"examples/{base}.valid.json")), True)
    check(f"{base}.invalid", schema, mreg, load(os.path.join(MODEL, f"examples/{base}.invalid.json")), False)

print("== query DSL examples ==")
dsl = load(os.path.join(QUERY, "dsl.schema.json"))
Draft202012Validator.check_schema(dsl)
qreg = registry_for(QUERY)
for f in sorted(glob.glob(os.path.join(QUERY, "examples/*.json"))):
    name = os.path.basename(f)
    check(name, dsl, qreg, load(f), should_pass=not name.startswith("invalid"))

print("== fields.json <-> model consistency ==")
# Every queryable field in fields.json must be a property of the corresponding
# model schema (a composite like "status.code" checks its base property "status").
fields = load(os.path.join(QUERY, "fields.json"))
target_schema = {
    "spans": load(os.path.join(MODEL, "span.schema.json")),
    "traces": load(os.path.join(MODEL, "trace.schema.json")),
    "scores": load(os.path.join(MODEL, "score.schema.json")),
}
for target, spec in fields["targets"].items():
    props = set(target_schema[target]["properties"].keys())
    for field in spec["fields"]:
        base = field["name"].split(".")[0]
        ok = base in props
        print(f"  [{'ok ' if ok else 'FAIL'}] {target}.{field['name']}" + ("" if ok else f"  (no '{base}' in {target} schema)"))
        if not ok:
            failures.append(f"{target}.{field['name']}")

if failures:
    print(f"\nFAILED: {len(failures)} check(s)")
    sys.exit(1)
print("\nALL SCHEMA CHECKS PASSED")
