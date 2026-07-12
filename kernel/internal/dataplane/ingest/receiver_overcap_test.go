package ingest

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/pipeline"
)

// TestOverCapBodyRejected413: an over-cap OTLP body must be REJECTED (413),
// never silently truncated and acked (which loses the tail of the batch while telling the
// client it all landed). An at-cap body still passes.
func TestOverCapBodyRejected413(t *testing.T) {
	// decodeBody unit: over-cap → 413, at-cap → 200.
	over := bytes.Repeat([]byte{'x'}, maxBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(over))
	if _, status := decodeBody(req); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-cap body must be 413, got %d", status)
	}
	atCap := bytes.Repeat([]byte{'x'}, maxBodyBytes)
	req = httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(atCap))
	if body, status := decodeBody(req); status != http.StatusOK || len(body) != maxBodyBytes {
		t.Fatalf("at-cap body must pass (200, full length), got %d len %d", status, len(body))
	}

	// End-to-end: an over-cap request 413s and enqueues NOTHING (no silent acked truncation).
	pipe := pipeline.New(nil, nil, nil, pipeline.NoopBus{}, pipeline.Config{})
	r := NewReceiver(pipe, slog.New(slog.NewTextHandler(io.Discard, nil)), 8, 1, nil)
	hreq := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(over))
	hreq.Header.Set("Authorization", "Bearer any")
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, hreq)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-cap export must be 413, got %d", w.Code)
	}
	if r.QueueLen() != 0 {
		t.Fatalf("an over-cap request must NOT enqueue a (truncated) job, got %d", r.QueueLen())
	}
}
