package ingest

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/pipeline"
)

// TestGRPCExportDoesNotTouchStorage mirrors the HTTP hot-path proof for gRPC: the
// Export RPC accepts, enqueues, and acks without any synchronous DB access. The
// pipeline has a nil store/pool and workers are not started, so a synchronous DB
// write would nil-deref rather than return a nil error + a queued job.
func TestGRPCExportDoesNotTouchStorage(t *testing.T) {
	pipe := pipeline.New(nil, nil, nil, pipeline.NoopBus{}, pipeline.Config{})
	r := NewReceiver(pipe, slog.New(slog.NewTextHandler(io.Discard, nil)), 8, 1, nil)
	svc := &grpcTraceService{r: r}

	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer my-key"))
	req := ptraceotlp.NewExportRequestFromTraces(ptrace.NewTraces())

	if _, err := svc.Export(ctx, req); err != nil {
		t.Fatalf("export should ack, got: %v", err)
	}
	if got := len(r.queue); got != 1 {
		t.Fatalf("expected exactly one enqueued job, got %d", got)
	}
	j := <-r.queue
	if j.bearer != "my-key" {
		t.Fatalf("bearer not extracted from metadata: %q", j.bearer)
	}
	if j.contentType != "application/x-protobuf" {
		t.Fatalf("gRPC jobs must be protobuf, got %q", j.contentType)
	}
}

// TestGRPCBackpressure: a saturated queue returns UNAVAILABLE so clients back off
// and retry into the idempotent merge (the gRPC mirror of the HTTP 503 envelope).
func TestGRPCBackpressure(t *testing.T) {
	pipe := pipeline.New(nil, nil, nil, pipeline.NoopBus{}, pipeline.Config{})
	r := NewReceiver(pipe, slog.New(slog.NewTextHandler(io.Discard, nil)), 1, 1, nil)
	svc := &grpcTraceService{r: r}
	req := ptraceotlp.NewExportRequestFromTraces(ptrace.NewTraces())

	if _, err := svc.Export(context.Background(), req); err != nil {
		t.Fatalf("first export should succeed: %v", err)
	}
	_, err := svc.Export(context.Background(), req)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("saturated queue should return Unavailable, got: %v", err)
	}
}
