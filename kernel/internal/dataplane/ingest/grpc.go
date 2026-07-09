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
// a job, so ack latency stays off the database (D13). Content-type is fixed to
// protobuf — the worker's decode path is transport-agnostic.
type grpcTraceService struct {
	ptraceotlp.UnimplementedGRPCServer
	r *Receiver
}

// Export enqueues the received traces and acks. On a full queue it returns
// ResourceExhausted so the client backs off and retries (mirrors the HTTP 503).
func (g *grpcTraceService) Export(ctx context.Context, req ptraceotlp.ExportRequest) (ptraceotlp.ExportResponse, error) {
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
	select {
	case g.r.queue <- j:
		return ptraceotlp.NewExportResponse(), nil
	default:
		return ptraceotlp.NewExportResponse(), status.Error(codes.ResourceExhausted, "ingestion queue full")
	}
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
