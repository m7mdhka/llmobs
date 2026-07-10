package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExposition(t *testing.T) {
	r := New()
	r.CounterAdd("llmobs_ingest_spans_total", "spans", map[string]string{"project_id": "p1", "kind": "generation"}, 3)
	r.CounterAdd("llmobs_ingest_spans_total", "spans", map[string]string{"project_id": "p1", "kind": "generation"}, 2)
	r.GaugeSet("llmobs_g", "g", nil, 7)
	r.Observe("llmobs_query_duration_seconds", "dur", map[string]string{"target": "spans"}, 0.03)
	r.SampledGauge("llmobs_sampled", "s", func() float64 { return 42 })

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `llmobs_ingest_spans_total{kind="generation",project_id="p1"} 5`) {
		t.Fatalf("counter not summed/labelled:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE llmobs_ingest_spans_total counter") {
		t.Fatalf("missing TYPE line:\n%s", body)
	}
	if !strings.Contains(body, "llmobs_query_duration_seconds_bucket{") || !strings.Contains(body, `le="+Inf"`) {
		t.Fatalf("histogram buckets missing:\n%s", body)
	}
	if !strings.Contains(body, "llmobs_query_duration_seconds_count{target=\"spans\"} 1") {
		t.Fatalf("histogram count missing:\n%s", body)
	}
	if !strings.Contains(body, "llmobs_sampled 42") {
		t.Fatalf("sampled gauge missing:\n%s", body)
	}
}

// A high-cardinality label is the caller's discipline, not the registry's — this
// test documents that the registry does not itself enforce it (labels are free).
func TestLabelsAreFree(t *testing.T) {
	r := New()
	r.CounterAdd("x", "", map[string]string{"project_id": "p"}, 1)
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `x{project_id="p"} 1`) {
		t.Fatal("expected labelled series")
	}
}
