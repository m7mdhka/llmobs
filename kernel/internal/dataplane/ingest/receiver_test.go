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

// TestHotPathDoesNotTouchStorage is the structural ack-path proof: the OTLP
// HTTP handler must accept, enqueue, and return WITHOUT any synchronous database
// access. The pipeline here is built with a nil store/pool and the worker pool is
// NOT started — so if the hot path did a synchronous DB write it would nil-deref
// (not return 200). Getting a 200 + a queued job proves the DB is off the hot path.
func TestHotPathDoesNotTouchStorage(t *testing.T) {
	pipe := pipeline.New(nil, nil, nil, pipeline.NoopBus{}, pipeline.Config{})
	r := NewReceiver(pipe, slog.New(slog.NewTextHandler(io.Discard, nil)), 8, 1, nil)
	// NOTE: workers deliberately not started.

	body := []byte(`{"resourceSpans":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer any")
	w := httptest.NewRecorder()

	r.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("hot path did not ack 200: got %d", w.Code)
	}
	if got := r.QueueLen(); got != 1 {
		t.Fatalf("expected exactly one enqueued job (no sync processing), got %d", got)
	}
}
