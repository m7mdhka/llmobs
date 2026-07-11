package normalize

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fixtureDir holds recorded (synthetic) OTLP traffic replayed through the
// normalizer and asserted against the expected canonical output (D3).
const fixtureDir = "../../../testdata/fixtures/otel-genai"

// TestSemConvFixture replays EVERY `<case>.otlp.json` under the fixture dir through
// the OTel GenAI normalizer and compares to its `<case>.expected.json` golden. Adding
// a fixture is dropping in a new pair — no code change. Regenerate all goldens with
// LLMOBS_UPDATE_FIXTURES=1.
func TestSemConvFixture(t *testing.T) {
	inputs, err := filepath.Glob(filepath.Join(fixtureDir, "*.otlp.json"))
	if err != nil {
		t.Fatalf("globbing fixtures: %v", err)
	}
	if len(inputs) == 0 {
		t.Fatal("no *.otlp.json fixtures found")
	}
	reg := Default()
	ctx := Context{ProjectID: "proj_demo"}

	for _, inPath := range inputs {
		name := strings.TrimSuffix(filepath.Base(inPath), ".otlp.json")
		t.Run(name, func(t *testing.T) {
			inBytes, err := os.ReadFile(inPath)
			if err != nil {
				t.Fatalf("reading fixture: %v", err)
			}
			td, err := UnmarshalOTLPJSON(inBytes)
			if err != nil {
				t.Fatalf("decoding OTLP JSON: %v", err)
			}
			var got []map[string]any
			for _, in := range FromTraces(td) {
				got = append(got, reg.Normalize(in, ctx))
			}
			sort.Slice(got, func(i, j int) bool {
				return got[i]["id"].(string) < got[j]["id"].(string)
			})

			goldenPath := filepath.Join(fixtureDir, name+".expected.json")
			pretty, _ := json.MarshalIndent(got, "", "  ")
			if os.Getenv("LLMOBS_UPDATE_FIXTURES") == "1" {
				if err := os.WriteFile(goldenPath, append(pretty, '\n'), 0o644); err != nil {
					t.Fatalf("writing golden: %v", err)
				}
				t.Logf("updated golden %s", goldenPath)
				return
			}
			wantBytes, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("reading golden (run with LLMOBS_UPDATE_FIXTURES=1 to create): %v", err)
			}
			var want []map[string]any
			if err := json.Unmarshal(wantBytes, &want); err != nil {
				t.Fatalf("parsing golden: %v", err)
			}
			if canonJSON(got) != canonJSON(want) {
				t.Errorf("normalized output != golden.\n got: %s\nwant: %s", canonJSON(got), canonJSON(want))
			}
		})
	}
}

func canonJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
