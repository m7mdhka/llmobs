package plugindata

import (
	"strings"
	"testing"
)

func idx(fields ...string) map[string]FieldSpec {
	m := map[string]FieldSpec{}
	for _, f := range fields {
		m[f] = FieldSpec{Name: f, Type: FieldString, Indexed: true}
	}
	return m
}

// TestCompileAlwaysScopesProject is the isolation invariant at the SQL layer:
// every compiled SELECT begins with project_id = $1, and $1 is the caller's
// project. No query shape can omit it.
func TestCompileAlwaysScopesProject(t *testing.T) {
	cases := []Query{
		{},
		{Filters: []Filter{{Field: "owner", Op: OpEq, Value: "u1"}}},
		{Order: &Order{Field: "title"}},
		{Filters: []Filter{{Field: "owner", Op: OpEq, Value: "u1"}}, Order: &Order{Field: "title", Desc: true}, Cursor: EncodeCursor("z", "id9")},
	}
	for i, q := range cases {
		sql, args, _, err := CompileSelect("plugin_acme__w", "dashboards", idx("owner", "title"), "projA", q)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if !strings.Contains(sql, `WHERE project_id = $1`) {
			t.Fatalf("case %d: missing project scope: %s", i, sql)
		}
		if len(args) == 0 || args[0] != "projA" {
			t.Fatalf("case %d: $1 must be the project, got %v", i, args)
		}
		if !strings.Contains(sql, `FROM "plugin_acme__w"."dashboards"`) {
			t.Fatalf("case %d: wrong table: %s", i, sql)
		}
	}
}

func TestCompileRejectsNonIndexedFields(t *testing.T) {
	// Filter on a non-indexed field.
	if _, _, _, err := CompileSelect("s", "c", idx("owner"), "p", Query{Filters: []Filter{{Field: "secret_notes", Op: OpEq, Value: "x"}}}); err == nil {
		t.Fatal("filter on non-indexed field must be rejected (R4)")
	}
	// Order on a non-indexed field.
	if _, _, _, err := CompileSelect("s", "c", idx("owner"), "p", Query{Order: &Order{Field: "secret_notes"}}); err == nil {
		t.Fatal("order on non-indexed field must be rejected (R4)")
	}
	// Unknown operator.
	if _, _, _, err := CompileSelect("s", "c", idx("owner"), "p", Query{Filters: []Filter{{Field: "owner", Op: "regex", Value: "x"}}}); err == nil {
		t.Fatal("unknown operator must be rejected")
	}
}

func TestCompileKeysetAndLimit(t *testing.T) {
	sql, _, limit, err := CompileSelect("s", "c", idx("title"), "p",
		Query{Order: &Order{Field: "title", Desc: false}, Cursor: EncodeCursor("m", "id5"), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if limit != 10 {
		t.Fatalf("limit = %d", limit)
	}
	// Ascending keyset: (title > $ OR (title = $ AND id > $)); fetch limit+1.
	if !strings.Contains(sql, `"title" > $`) || !strings.Contains(sql, `id > $`) {
		t.Fatalf("keyset predicate missing: %s", sql)
	}
	if !strings.Contains(sql, `ORDER BY "title" ASC, id ASC`) || !strings.HasSuffix(sql, "LIMIT 11") {
		t.Fatalf("order/limit wrong: %s", sql)
	}
}

func TestCompileLimitCap(t *testing.T) {
	_, _, limit, _ := CompileSelect("s", "c", idx(), "p", Query{Limit: 100000})
	if limit != MaxLimit {
		t.Fatalf("limit not capped: %d", limit)
	}
}

func TestCollectionValidate(t *testing.T) {
	ok := CollectionSpec{Name: "dashboards", Fields: []FieldSpec{
		{Name: "title", Type: FieldString, Indexed: true},
		{Name: "config", Type: FieldJSON},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid collection rejected: %v", err)
	}
	bad := []CollectionSpec{
		{Name: "Bad-Name", Fields: nil},                                                                  // invalid name
		{Name: "c", Fields: []FieldSpec{{Name: "1x", Type: FieldString}}},                                // invalid field
		{Name: "c", Fields: []FieldSpec{{Name: "f", Type: "weird"}}},                                     // bad type
		{Name: "c", Fields: []FieldSpec{{Name: "f", Type: FieldJSON, Indexed: true}}},                    // json indexed
		{Name: "c", Fields: []FieldSpec{{Name: "f", Type: FieldString}, {Name: "f", Type: FieldString}}}, // dup
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Fatalf("bad collection %d passed validation", i)
		}
	}
}

func TestSchemaName(t *testing.T) {
	got, err := SchemaName("acme/my-widget")
	if err != nil {
		t.Fatal(err)
	}
	if got != "plugin_acme__my_widget" {
		t.Fatalf("schema name = %q", got)
	}
	if _, err := SchemaName("Bad/Id"); err == nil {
		t.Fatal("invalid plugin id must be rejected")
	}
}
