package normalize

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// fixtureDir holds recorded (synthetic) OTLP traffic replayed through the
// normalizer and asserted against the expected canonical output (D3).
const fixtureDir = "../../../testdata/fixtures/otel-genai"

// TestSemConvFixture replays agent-trace.otlp.json through the OTel GenAI
// normalizer and compares to the golden agent-trace.expected.json. Regenerate the
// golden with LLMOBS_UPDATE_FIXTURES=1.
func TestSemConvFixture(t *testing.T) {
	inBytes, err := os.ReadFile(filepath.Join(fixtureDir, "agent-trace.otlp.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	td, err := UnmarshalOTLPJSON(inBytes)
	if err != nil {
		t.Fatalf("decoding OTLP JSON: %v", err)
	}
	reg := Default()
	ctx := Context{ProjectID: "proj_demo"}

	var got []map[string]any
	for _, in := range FromTraces(td) {
		got = append(got, reg.Normalize(in, ctx))
	}
	sort.Slice(got, func(i, j int) bool {
		return got[i]["id"].(string) < got[j]["id"].(string)
	})

	goldenPath := filepath.Join(fixtureDir, "agent-trace.expected.json")
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
}

func canonJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
