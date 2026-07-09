// Package ingest hosts the OTLP receivers. The HTTP hot path is async-ack: it
// authenticates nothing and writes nothing to the database — it reads the body,
// enqueues it in-process, and returns, so ack latency has no synchronous DB
// dependency (D13 ack budget). A worker pool drains the queue and runs the full
// pipeline (authenticate -> ... -> persist).
package ingest

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"

	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/pipeline"
)

const maxBodyBytes = 4 << 20 // 4 MiB request cap

// job is a received-but-not-yet-processed request. It carries only bytes and the
// bearer — no DB handle — so the hot path stays off the database.
type job struct {
	body        []byte
	contentType string
	bearer      string
	receivedAt  time.Time
}

// Receiver is the OTLP HTTP receiver + async worker pool.
type Receiver struct {
	queue   chan job
	pipe    *pipeline.Pipeline
	log     *slog.Logger
	workers int
	wg      sync.WaitGroup
}

// NewReceiver builds a receiver with an in-process queue and worker pool.
func NewReceiver(pipe *pipeline.Pipeline, log *slog.Logger, queueSize, workers int) *Receiver {
	if queueSize <= 0 {
		queueSize = 1024
	}
	if workers <= 0 {
		workers = 4
	}
	return &Receiver{queue: make(chan job, queueSize), pipe: pipe, log: log, workers: workers}
}

// Handler returns the OTLP/HTTP traces handler (mount at /v1/traces). It is the
// hot path: read body -> enqueue -> ack. No database access.
func (r *Receiver) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, maxBodyBytes))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		bearer := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		ct := req.Header.Get("Content-Type")
		j := job{body: body, contentType: ct, bearer: bearer, receivedAt: time.Now()}
		select {
		case r.queue <- j:
		default:
			// Backpressure: queue full. Signal retryable so the client backs off.
			http.Error(w, "ingestion queue full", http.StatusServiceUnavailable)
			return
		}
		writeExportResponse(w, ct)
	})
	return mux
}

func writeExportResponse(w http.ResponseWriter, contentType string) {
	resp := ptraceotlp.NewExportResponse()
	if strings.Contains(strings.ToLower(contentType), "application/json") {
		b, _ := resp.MarshalJSON()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
		return
	}
	b, _ := resp.MarshalProto()
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// Start launches the worker pool. Workers drain the queue and run the pipeline.
func (r *Receiver) Start(ctx context.Context) {
	for i := 0; i < r.workers; i++ {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-r.queue:
					r.process(ctx, j)
				}
			}
		}()
	}
}

// Stop drains and waits for workers to finish (best-effort on shutdown).
func (r *Receiver) Stop() { r.wg.Wait() }

func (r *Receiver) process(ctx context.Context, j job) {
	ing := &pipeline.Ingestion{
		Bearer:      j.bearer,
		Body:        j.body,
		ContentType: j.contentType,
		ReceivedAt:  j.receivedAt,
	}
	if err := r.pipe.Run(ctx, ing); err != nil {
		r.log.Warn("ingestion pipeline error", "err", err.Error())
	}
}
