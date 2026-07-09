package postgres

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// specPath is the single source of the normative merge vectors. The suite parses
// V1–V16 straight out of the spec (no fixture duplication) and runs them against
// the adapter's Fold — the cross-adapter conformance suite's first citizen.
const specRelPath = "../../../../api/model/v1alpha1/05-update-semantics.md"

type vectorEvent struct {
	Op      string         `json:"op"`
	EventTS float64        `json:"event_ts"`
	EventID string         `json:"event_id"`
	Payload map[string]any `json:"payload"`
}

type vector struct {
	Name   string         `json:"name"`
	Entity string         `json:"entity"`
	Note   string         `json:"note"`
	Events []vectorEvent  `json:"events"`
	Expect map[string]any `json:"expect"`
}

var jsonBlock = regexp.MustCompile("(?s)```json\\n(.*?)\\n```")

func loadVectors(t *testing.T) []vector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(specRelPath))
	if err != nil {
		t.Fatalf("reading spec %s: %v", specRelPath, err)
	}
	matches := jsonBlock.FindAllStringSubmatch(string(raw), -1)
	if len(matches) == 0 {
		t.Fatalf("no JSON vector blocks found in %s", specRelPath)
	}
	var vs []vector
	for _, m := range matches {
		var v vector
		if err := json.Unmarshal([]byte(m[1]), &v); err != nil {
			t.Fatalf("parsing vector block: %v\n%s", err, m[1])
		}
		vs = append(vs, v)
	}
	return vs
}

func TestMergeVectorsFromSpec(t *testing.T) {
	vs := loadVectors(t)
	if len(vs) < 16 {
		t.Fatalf("expected >= 16 vectors, parsed %d", len(vs))
	}
	for _, v := range vs {
		v := v
		t.Run(v.Name, func(t *testing.T) {
			events := make([]Event, len(v.Events))
			for i, e := range v.Events {
				events[i] = Event{
					Op:      Op(e.Op),
					EventTS: time.Unix(int64(e.EventTS), 0).UTC(),
					EventID: e.EventID,
					Payload: e.Payload,
				}
			}
			got := Fold(v.Entity, events)
			if !canonEqual(got, v.Expect) {
				t.Errorf("%s (%s)\n  expect: %s\n  got   : %s", v.Name, v.Note, canon(v.Expect), canon(got))
			}
		})
	}
}

func canon(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func canonEqual(a, b any) bool { return canon(a) == canon(b) }
