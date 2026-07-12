package ingest

import (
	"context"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// grpcTraceService implements the OTLP/gRPC TracesService. Like the HTTP hot
// path it does no DB work: it marshals the request to proto bytes and enqueues
// a job, so ack latency stays off the database. Content-type is fixed to
// protobuf — the worker's decode path is transport-agnostic.
type grpcTraceService struct {
	ptraceotlp.UnimplementedGRPCServer
	r *Receiver
}

// Export enqueues the received traces and acks. Under backpressure (shutting
// down, persistence unhealthy, or the queue saturated) it returns UNAVAILABLE so
// the client backs off and retries into the idempotent merge — the gRPC mirror of
// the HTTP 503+Retry-After envelope.
func (g *grpcTraceService) Export(ctx context.Context, req ptraceotlp.ExportRequest) (ptraceotlp.ExportResponse, error) {
	if reason := g.r.backpressure(); reason != "" {
		g.r.shed(reason)
		return ptraceotlp.NewExportResponse(), status.Error(codes.Unavailable, "ingestion unavailable: "+reason)
	}
	body, err := req.MarshalProto()
	if err != nil {
		return ptraceotlp.NewExportResponse(), status.Error(codes.InvalidArgument, "malformed export request")
	}
	j := job{
		body:        body,
		contentType: "application/x-protobuf",
		bearer:      bearerFromMetadata(ctx),
		receivedAt:  time.Now(),
	}
	// Durable-append for the WAL spool (ack-after-durable); ErrSpoolFull sheds.
	if err := g.r.spool.Append(j); err != nil {
		g.r.shed("queue_full")
		return ptraceotlp.NewExportResponse(), status.Error(codes.Unavailable, "ingestion unavailable: queue_full")
	}
	return ptraceotlp.NewExportResponse(), nil
}

// bearerFromMetadata pulls the token from the gRPC "authorization" metadata,
// stripping an optional "Bearer " prefix (OTLP exporters send it either way).
func bearerFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return ""
	}
	tok := vals[0]
	if len(tok) > 7 && (tok[:7] == "Bearer " || tok[:7] == "bearer ") {
		return tok[7:]
	}
	return tok
}

// RegisterGRPC registers the OTLP traces service on the given gRPC server.
func (r *Receiver) RegisterGRPC(s *grpc.Server) {
	ptraceotlp.RegisterGRPCServer(s, &grpcTraceService{r: r})
}
