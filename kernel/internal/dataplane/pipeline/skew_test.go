package pipeline

import (
	"testing"
	"time"
)

func TestStampClockSkew(t *testing.T) {
	recv := time.Unix(1_000_000, 0).UTC()
	threshold := 5 * time.Minute

	// Within threshold: no signal.
	c := map[string]any{"attributes": map[string]any{}}
	stampClockSkew(c, recv.Add(2*time.Minute), recv, threshold)
	if _, ok := c["attributes"].(map[string]any)["llmobs.dq.clock_skew"]; ok {
		t.Fatal("within-threshold drift should not stamp skew")
	}

	// Fast clock beyond threshold: signal with the signed delta (seconds).
	c = map[string]any{"attributes": map[string]any{}}
	stampClockSkew(c, recv.Add(20*time.Minute), recv, threshold)
	v, ok := c["attributes"].(map[string]any)["llmobs.dq.clock_skew"].(float64)
	if !ok || v != 1200 {
		t.Fatalf("expected +1200s skew, got %v", c["attributes"])
	}

	// Slow clock (event before receive) beyond threshold: negative delta.
	c = map[string]any{"attributes": map[string]any{}}
	stampClockSkew(c, recv.Add(-20*time.Minute), recv, threshold)
	if v, _ := c["attributes"].(map[string]any)["llmobs.dq.clock_skew"].(float64); v != -1200 {
		t.Fatalf("expected -1200s skew, got %v", c["attributes"])
	}

	// Disabled (zero threshold): never stamps.
	c = map[string]any{"attributes": map[string]any{}}
	stampClockSkew(c, recv.Add(time.Hour), recv, 0)
	if len(c["attributes"].(map[string]any)) != 0 {
		t.Fatal("zero threshold disables detection")
	}
}
