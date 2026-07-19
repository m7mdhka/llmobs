package query

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fields.json is the query-surface contract. compile.go carries the compiler's own
// per-target field maps. Both files have long CLAIMED "a CI consistency check asserts
// this" — this is that check. It closes a real drift class a second incumbent shipped:
// a field the contract accepts that a given query path silently ignores instead of
// evaluating or rejecting. The invariant enforced here: every contract field is EITHER
// evaluable on every path OR rejected at validate time (unknown_field 422) — never
// accepted-then-dropped.

// fieldsJSONRelPath points at the contract from this package dir:
// kernel/internal/dataplane/query -> repo root -> api.
const fieldsJSONRelPath = "../../../../api/query/v1alpha1/fields.json"

type contractField struct {
	Name      string `json:"name"`
	Class     string `json:"class"`
	ValueType string `json:"value_type"`
	Orderable bool   `json:"orderable"`
	Groupable bool   `json:"groupable"`
	Deferred  bool   `json:"deferred"`
}

type contractDoc struct {
	Targets map[string]struct {
		TimeAnchor string          `json:"timeAnchor"`
		Fields     []contractField `json:"fields"`
	} `json:"targets"`
}

// expectedClass maps a contract (class, value_type) pair to the compiler's fieldClass.
// A mismatch here is exactly the drift the test exists to catch (e.g. a decimal field
// wired as a string column, or a numeric map wired as a text map).
func expectedClass(f contractField) (fieldClass, bool) {
	switch f.Class {
	case "string":
		return classString, true
	case "enum":
		return classEnum, true
	case "numeric", "decimal":
		return classNumeric, true
	case "timestamp":
		return classTimestamp, true
	case "boolean":
		return classBoolean, true
	case "reference":
		return classReference, true
	case "attr_map":
		if f.ValueType == "numeric" {
			return classNumericMap, true
		}
		return classAttrMap, true
	case "string_array":
		// No compiler class exists yet; only a deferred field may carry it.
		return 0, false
	}
	return 0, false
}

func loadContract(t *testing.T) contractDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(fieldsJSONRelPath))
	if err != nil {
		t.Fatalf("reading fields.json: %v", err)
	}
	var doc contractDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing fields.json: %v", err)
	}
	if len(doc.Targets) == 0 {
		t.Fatalf("fields.json has no targets")
	}
	return doc
}

func compilerFieldsFor(t *testing.T, target string) queryFields {
	t.Helper()
	qf, err := fieldsForTarget(target)
	if err != nil {
		t.Fatalf("no compiler fields for target %q: %v", target, err)
	}
	return qf
}

// TestContractMatchesCompiler asserts, per target, that the contract and the compiler
// agree field-for-field: a non-deferred field is present with the right class and the
// right orderable/groupable flags; a deferred field is ABSENT (so the compiler 422s it).
func TestContractMatchesCompiler(t *testing.T) {
	doc := loadContract(t)
	for target, spec := range doc.Targets {
		qf := compilerFieldsFor(t, target)

		// timeAnchor agreement.
		if spec.TimeAnchor != qf.timeAnchor {
			t.Errorf("[%s] timeAnchor: contract %q, compiler %q", target, spec.TimeAnchor, qf.timeAnchor)
		}

		contractNames := map[string]bool{}
		for _, f := range spec.Fields {
			contractNames[f.Name] = true
			def, inCompiler := qf.fields[f.Name]

			if f.Deferred {
				if inCompiler {
					t.Errorf("[%s] field %q is marked deferred in the contract but IS in the compiler map — a deferred field must be rejected, not evaluated", target, f.Name)
				}
				continue
			}

			if !inCompiler {
				t.Errorf("[%s] contract field %q is not in the compiler map (accepted-by-contract, unevaluable) — evaluate it or mark it deferred", target, f.Name)
				continue
			}
			wantClass, ok := expectedClass(f)
			if !ok {
				t.Errorf("[%s] field %q has class %q/value_type %q with no compiler class — must be deferred", target, f.Name, f.Class, f.ValueType)
				continue
			}
			if def.class != wantClass {
				t.Errorf("[%s] field %q class drift: contract implies %d, compiler has %d", target, f.Name, wantClass, def.class)
			}
			if qf.orderable[f.Name] != f.Orderable {
				t.Errorf("[%s] field %q orderable drift: contract %v, compiler %v", target, f.Name, f.Orderable, qf.orderable[f.Name])
			}
			if qf.groupable[f.Name] != f.Groupable {
				t.Errorf("[%s] field %q groupable drift: contract %v, compiler %v", target, f.Name, f.Groupable, qf.groupable[f.Name])
			}
		}

		// Reverse direction: every compiler field must be a (non-deferred) contract
		// field. A compiler field missing from the contract is an undocumented surface.
		for name := range qf.fields {
			if !contractNames[name] {
				t.Errorf("[%s] compiler field %q is not in the contract (fields.json) — undocumented query surface", target, name)
			}
		}
	}
}

// TestDeferredAndUnknownFieldsAreRejectedEverywhere is the accept-or-reject proof: for
// every deferred contract field and an obviously-unknown field, a filter, orderBy,
// groupBy, and aggregation each return a 422 CompileError — never a nil error (which
// would mean the field was accepted and then silently dropped).
func TestDeferredAndUnknownFieldsAreRejectedEverywhere(t *testing.T) {
	doc := loadContract(t)
	window := time.Hour * 24 * 365
	from := "2026-01-01T00:00:00Z"
	to := "2026-01-02T00:00:00Z"
	timeRange := map[string]any{"from": from, "to": to}

	compileRow := map[string]func(map[string]any) error{
		"spans":  func(d map[string]any) error { _, e := CompileSpans(d, "p", window); return e },
		"traces": func(d map[string]any) error { _, e := CompileTraces(d, "p", window); return e },
		"scores": func(d map[string]any) error { _, e := CompileScores(d, "p", window); return e },
	}

	assert422 := func(t *testing.T, label string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: expected a 422 rejection, got nil (field was accepted then silently dropped)", label)
			return
		}
		ce, ok := err.(*CompileError)
		if !ok {
			t.Errorf("%s: expected *CompileError, got %T (%v)", label, err, err)
			return
		}
		if ce.Status != 422 {
			t.Errorf("%s: expected HTTP 422, got %d (%s)", label, ce.Status, ce.Code)
		}
	}

	for target, spec := range doc.Targets {
		// Collect the names that MUST be rejected on this target: every deferred
		// contract field, plus a guaranteed-unknown name.
		reject := []string{"__definitely_not_a_field__"}
		for _, f := range spec.Fields {
			if f.Deferred {
				reject = append(reject, f.Name)
			}
		}
		rowCompile := compileRow[target]

		for _, name := range reject {
			// Path 1: filter condition.
			assert422(t, target+"/filter/"+name, rowCompile(map[string]any{
				"target": target, "timeRange": timeRange,
				"filters": []any{map[string]any{"field": name, "op": "eq", "value": "x"}},
			}))

			// Path 2: orderBy.
			assert422(t, target+"/orderBy/"+name, rowCompile(map[string]any{
				"target": target, "timeRange": timeRange,
				"orderBy": []any{map[string]any{"field": name, "dir": "asc"}},
			}))

			// Path 3: groupBy (aggregation query).
			_, gErr := CompileAggregation(map[string]any{
				"target": target, "timeRange": timeRange,
				"groupBy":      []any{name},
				"aggregations": []any{map[string]any{"op": "count"}},
			}, "p", window, target)
			assert422(t, target+"/groupBy/"+name, gErr)

			// Path 4: aggregation field.
			_, aErr := CompileAggregation(map[string]any{
				"target": target, "timeRange": timeRange,
				"aggregations": []any{map[string]any{"op": "max", "field": name}},
			}, "p", window, target)
			assert422(t, target+"/aggField/"+name, aErr)
		}
	}
}
