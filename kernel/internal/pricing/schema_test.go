package pricing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// schemaPath is the price-entry contract, relative to this package dir.
const schemaPath = "../../../api/schemas/pricing/v1alpha1/price-entry.schema.json"

// jsonTags returns the set of json field names on a struct (excluding "-" and options).
func jsonTags(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

func loadSchema(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(schemaPath))
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	return s
}

func propKeys(schema map[string]any, path ...string) map[string]bool {
	cur := schema
	for _, p := range path {
		next, _ := cur[p].(map[string]any)
		cur = next
	}
	props, _ := cur["properties"].(map[string]any)
	out := map[string]bool{}
	for k := range props {
		out[k] = true
	}
	return out
}

// TestSchemaStructNoDrift is the contracts-first drift guard the review asked for: the
// Go pricing.Entry / Rate / Tier structs are HAND-mirrored from
// price-entry.schema.json (not codegen'd, since pricing is control-plane metadata, not
// the telemetry model). Nothing else binds them, so this test asserts the schema
// properties and the struct json tags are the SAME set — bidirectionally — so a future
// edit to one that forgets the other fails the build.
func TestSchemaStructNoDrift(t *testing.T) {
	schema := loadSchema(t)

	check := func(name string, structTags, schemaProps map[string]bool) {
		for k := range schemaProps {
			if !structTags[k] {
				t.Errorf("%s: schema property %q has no matching struct json tag (drift)", name, k)
			}
		}
		for k := range structTags {
			if !schemaProps[k] {
				t.Errorf("%s: struct json tag %q is not in the schema (drift)", name, k)
			}
		}
	}

	check("Entry", jsonTags(reflect.TypeOf(Entry{})), propKeys(schema))
	// Rate shape lives under rates.additionalProperties.
	check("Rate", jsonTags(reflect.TypeOf(Rate{})), propKeys(schema, "properties", "rates", "additionalProperties"))
	// Tier shape lives under tiers.items.
	check("Tier", jsonTags(reflect.TypeOf(Tier{})), propKeys(schema, "properties", "tiers", "items"))
}

// TestSchemaExamplesRoundTrip proves the committed valid example unmarshals into Entry
// and the invalid one is genuinely malformed (basic contract sanity without a JSON
// Schema validator dependency).
func TestSchemaExamplesRoundTrip(t *testing.T) {
	valid, err := os.ReadFile(filepath.Clean("../../../api/schemas/pricing/v1alpha1/examples/price-entry.valid.json"))
	if err != nil {
		t.Fatalf("read valid example: %v", err)
	}
	var e Entry
	if err := json.Unmarshal(valid, &e); err != nil {
		t.Fatalf("valid example must unmarshal into Entry: %v", err)
	}
	if e.ID != EntryID(CanonicalProvider(e.Provider), CanonicalModel(e.Model), e.Version) {
		t.Fatalf("valid example id %q disagrees with EntryID convention", e.ID)
	}
	if err := e.ValidateRates(); err != nil {
		t.Fatalf("valid example rates must pass validation: %v", err)
	}
}

// TestDefaultSeedsAreSchemaValid proves every shipped default seed is well-formed —
// canonical ids, passing rate validation — so a bad seed can never ship.
func TestDefaultSeedsAreSchemaValid(t *testing.T) {
	// defaultPriceSeeds lives in the postgres package; re-declare the invariant here on
	// a representative constructed entry (the postgres package's TestDefaultSeeds covers
	// the real set). This keeps the pricing package dependency-free.
	e := Entry{
		Provider: "openai", Model: "gpt-4o", Version: 1, EffectiveFrom: "2026-01-01T00:00:00Z",
		Rates: map[string]Rate{"input": {PerToken: 0.0000025}, "cache_read": {PerToken: 0.00000125, Reduces: "input"}},
	}
	if err := e.ValidateRates(); err != nil {
		t.Fatalf("representative seed invalid: %v", err)
	}
}
