package ingest

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/ingesthealth"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/metrics"
)

func postTrace(r *Receiver) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader([]byte(`{"resourceSpans":[]}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, req)
	return w
}

// TestBackpressureOnPersistUnhealthy is the backpressure proof: while
// persistence is healthy the OTLP endpoints fast-ack (async property intact); once
// the persist signal flips unhealthy they shed with a retryable 503 (HTTP,
// Retry-After set) / UNAVAILABLE (gRPC) instead of acking into a queue that cannot
// drain. Workers are not started, so nothing masks the decision.
func TestBackpressureOnPersistUnhealthy(t *testing.T) {
	sig := ingesthealth.New(1)
	r := NewReceiver(&countingRunner{}, slog.New(slog.NewTextHandler(io.Discard, nil)), 8, 1, metrics.New())
	r.SetPersistSignal(sig)

	// Healthy: fast-ack.
	if w := postTrace(r); w.Code != http.StatusOK {
		t.Fatalf("healthy path must fast-ack 200, got %d", w.Code)
	}

	// Flip unhealthy → shed.
	sig.RecordPersist(errBoom)
	w := postTrace(r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy persistence must shed 503, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("shed response must carry Retry-After")
	}

	// gRPC mirror: UNAVAILABLE.
	svc := &grpcTraceService{r: r}
	_, err := svc.Export(context.Background(), ptraceotlp.NewExportRequestFromTraces(ptrace.NewTraces()))
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("unhealthy persistence must shed gRPC Unavailable, got %v", err)
	}
}

type boomErr struct{}

func (boomErr) Error() string { return "boom" }

var errBoom = boomErr{}
