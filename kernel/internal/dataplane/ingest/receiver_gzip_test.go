package ingest

import (
	"bytes"
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/pipeline"
)

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestDecodeBodyGzip: a gzipped body is transparently decompressed (OTLP
// exporters gzip by default), an over-cap decompressed stream is REJECTED 413 (gzip-bomb
// bound, never buffered), and a body that claims gzip but isn't is a clean 400.
func TestDecodeBodyGzip(t *testing.T) {
	payload := []byte(`{"resourceSpans":[]}`)

	t.Run("gzip decoded", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(gzipBytes(t, payload)))
		req.Header.Set("Content-Encoding", "gzip")
		body, status := decodeBody(req)
		if status != http.StatusOK || !bytes.Equal(body, payload) {
			t.Fatalf("gzip body must decode to the payload, got status %d body %q", status, body)
		}
	})

	t.Run("gzip bomb rejected 413", func(t *testing.T) {
		// A tiny compressed body that inflates past the cap must be bounded, not buffered.
		bomb := gzipBytes(t, bytes.Repeat([]byte{'a'}, maxBodyBytes+1024))
		if len(bomb) > maxBodyBytes {
			t.Fatalf("test bomb should compress small, got %d", len(bomb))
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(bomb))
		req.Header.Set("Content-Encoding", "gzip")
		if _, status := decodeBody(req); status != http.StatusRequestEntityTooLarge {
			t.Fatalf("over-cap decompressed body must be 413, got %d", status)
		}
	})

	t.Run("corrupt gzip -> 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader([]byte("not gzip at all")))
		req.Header.Set("Content-Encoding", "gzip")
		if _, status := decodeBody(req); status != http.StatusBadRequest {
			t.Fatalf("a body that claims gzip but isn't must be 400, got %d", status)
		}
	})

	t.Run("plain body unchanged", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(payload))
		if body, status := decodeBody(req); status != http.StatusOK || !bytes.Equal(body, payload) {
			t.Fatalf("plain body must pass through, got status %d", status)
		}
	})
}

// TestGzipRoundTripThroughHandler: end-to-end, a gzipped OTLP export acks 200 and enqueues
// exactly one job (proving the decoded body reaches the queue).
func TestGzipRoundTripThroughHandler(t *testing.T) {
	pipe := pipeline.New(nil, nil, nil, pipeline.NoopBus{}, pipeline.Config{})
	r := NewReceiver(pipe, slog.New(slog.NewTextHandler(io.Discard, nil)), 8, 1, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(gzipBytes(t, []byte(`{"resourceSpans":[]}`))))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer any")
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("gzipped export must ack 200, got %d", w.Code)
	}
	if r.QueueLen() != 1 {
		t.Fatalf("expected one enqueued job, got %d", r.QueueLen())
	}
}
