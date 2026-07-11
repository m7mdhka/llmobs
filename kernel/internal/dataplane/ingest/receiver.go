// Package ingest hosts the OTLP receivers. The HTTP hot path is async-ack: it
// authenticates nothing and writes nothing to the database — it reads the body,
// enqueues it in-process, and returns, so ack latency has no synchronous DB
// dependency (D13 ack budget). A worker pool drains the queue and runs the full
// pipeline (authenticate -> ... -> persist).
//
// Async-ack has one honest cost: a job that is acked but not yet persisted lives
// only in the in-process queue. On shutdown we DRAIN that queue before exiting
// (G1) so a rolling deploy does not silently shed already-acked spans; the only
// residual loss window is a forced termination that outlives the drain deadline,
// which is counted (not silent). Under persist failure or saturation the
// receivers shed with a retryable 503/UNAVAILABLE (G2) rather than acking into a
// queue that cannot drain.
package ingest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"

	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/ingesthealth"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/pipeline"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/metrics"
)

const maxBodyBytes = 4 << 20 // 4 MiB request cap

// highWaterFraction is the queue-occupancy threshold (of capacity) at which the
// receivers start shedding new work with a retryable 503 instead of acking it
// (G2). Set at 80%: it leaves headroom so requests already past the check don't
// hard-fail at the channel, and it gives clients an early, honest backpressure
// signal well before the queue is truly full — turning "false 200 then silent
// loss" into "503 then client retry into the idempotent merge".
const highWaterFraction = 0.8

// job is a received-but-not-yet-processed request. It carries only bytes and the
// bearer — no DB handle — so the hot path stays off the database.
type job struct {
	body        []byte
	contentType string
	bearer      string
	receivedAt  time.Time
}

// Runner is the pipeline surface the receiver drains into (interface-at-consumer,
// go-style): the concrete *pipeline.Pipeline satisfies it, and tests inject a fake
// so drain/backpressure can be exercised without a database.
type Runner interface {
	Run(ctx context.Context, ing *pipeline.Ingestion) error
}

// Receiver is the OTLP HTTP receiver + async worker pool.
type Receiver struct {
	spool   Spool
	pipe    Runner
	log     *slog.Logger
	workers int
	mreg    *metrics.Registry
	signal  *ingesthealth.Signal // persist-health gate (nil => no backpressure on health)

	wg         sync.WaitGroup
	draining   atomic.Bool
	drainC     chan struct{}
	drainOnce  sync.Once
	hardCtx    context.Context
	hardCancel context.CancelFunc
}

// NewReceiver builds a receiver with an in-memory spool and worker pool (lite
// default). Call SetSpool to swap in the durable WAL spool for the scale profile.
// mreg may be nil (instrumentation disabled). The persist-health signal is attached
// separately via SetPersistSignal so backpressure-on-health is opt-in (G2).
func NewReceiver(pipe Runner, log *slog.Logger, queueSize, workers int, mreg *metrics.Registry) *Receiver {
	if queueSize <= 0 {
		queueSize = 1024
	}
	if workers <= 0 {
		workers = 4
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Receiver{
		spool:      newMemSpool(queueSize),
		pipe:       pipe,
		log:        log,
		workers:    workers,
		mreg:       mreg,
		drainC:     make(chan struct{}),
		hardCtx:    ctx,
		hardCancel: cancel,
	}
}

// SetSpool swaps the ack-window implementation (ADR-0027) — e.g. the durable WAL
// spool in the scale profile. Must be called before Start.
func (r *Receiver) SetSpool(s Spool) { r.spool = s }

// SetPersistSignal attaches the persist-health gate used for readiness-consistent
// backpressure (G2). When the signal reports unhealthy, the receivers shed new
// work with a retryable 503/UNAVAILABLE instead of acking it.
func (r *Receiver) SetPersistSignal(sig *ingesthealth.Signal) { r.signal = sig }

// QueueLen / QueueCap expose spool occupancy for scrape-time gauges.
func (r *Receiver) QueueLen() int { return r.spool.Len() }
func (r *Receiver) QueueCap() int { return r.spool.Cap() }

// highWater is the spool-occupancy threshold (of capacity) at which new work is
// shed (G2), computed from the current spool capacity.
func (r *Receiver) highWater() int {
	hw := int(float64(r.spool.Cap()) * highWaterFraction)
	if hw < 1 {
		hw = 1
	}
	return hw
}

// backpressure returns a non-empty reason when new work must be shed (503/
// UNAVAILABLE) rather than acked: shutting down, persistence unhealthy, or the
// spool past its high-water mark. Empty string => accept on the fast path.
func (r *Receiver) backpressure() string {
	switch {
	case r.draining.Load():
		return "draining"
	case r.signal != nil && !r.signal.Healthy():
		return "persist_unhealthy"
	case r.spool.Len() >= r.highWater():
		return "high_water"
	default:
		return ""
	}
}

// shed records a backpressure rejection (by reason) for the operator dashboard.
func (r *Receiver) shed(reason string) {
	if r.mreg != nil {
		r.mreg.CounterAdd("llmobs_ingest_backpressure_shed_total",
			"OTLP requests shed with a retryable 503/UNAVAILABLE under backpressure, by reason.",
			map[string]string{"reason": reason}, 1)
	}
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
		// Backpressure envelope: reject cheaply before reading the body when we are
		// shutting down, persistence is unhealthy, or the queue is saturated.
		if reason := r.backpressure(); reason != "" {
			r.shed(reason)
			retryable503(w, reason)
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
		// Append is durable for the WAL spool (fsync before return), so the ack
		// below is ack-after-durable. ErrSpoolFull is the last-resort backpressure
		// (the spool filled between the check and here).
		if err := r.spool.Append(j); err != nil {
			r.shed("queue_full")
			retryable503(w, "queue_full")
			return
		}
		writeExportResponse(w, ct)
	})
	return mux
}

// retryable503 signals a retryable rejection with a conservative Retry-After so
// OTLP exporters back off and retry into the idempotent merge.
func retryable503(w http.ResponseWriter, reason string) {
	w.Header().Set("Retry-After", "1")
	http.Error(w, "ingestion unavailable: "+reason, http.StatusServiceUnavailable)
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

// Start launches the worker pool. Workers run on the receiver's own context, NOT
// the caller's — a shutdown signal must not kill queued-but-unpersisted work.
// DrainAndWait drives the ordered shutdown (stop accepting -> drain -> deadline).
func (r *Receiver) Start(_ context.Context) {
	for i := 0; i < r.workers; i++ {
		r.wg.Add(1)
		go r.worker()
	}
}

func (r *Receiver) worker() {
	defer r.wg.Done()
	for {
		// Priority guard: once the hard deadline has fired, stop immediately rather
		// than let select randomly pull (and abandon) more queued jobs — so the
		// residual count is exactly what remains buffered, not fewer.
		if r.hardCtx.Err() != nil {
			return
		}
		select {
		case <-r.hardCtx.Done():
			return
		case <-r.drainC:
			r.finalDrain()
			return
		default:
		}
		l, ok := r.spool.Next(r.hardCtx)
		if !ok {
			return
		}
		r.handle(r.hardCtx, l)
	}
}

// finalDrain processes everything already buffered, then returns — unless the hard
// deadline elapsed (hardCtx cancelled), in which case it stops immediately and
// DrainAndWait accounts for whatever remains.
func (r *Receiver) finalDrain() {
	for {
		if r.hardCtx.Err() != nil {
			return
		}
		l, ok := r.spool.tryNext()
		if !ok {
			return
		}
		r.handle(r.hardCtx, l)
	}
}

// handle runs one leased record through the pipeline and settles it on the spool.
// The failure classification is load-bearing for durability (ADR-0027 D6):
//   - success / erasure-suppressed (pipe.Run returns nil for both) → Commit.
//   - a PERMANENT error (malformed body — pipeline.ErrPermanent) → dead-letter: it
//     can never succeed, so drop it and advance past it.
//   - any OTHER error is TRANSIENT (a DB/infra outage): NEVER drop and NEVER commit
//     it — that would advance the watermark past durable data and vaporize it on the
//     next truncate. Back off and requeue; the record stays durable and replays,
//     and G2 sheds new ingest until persistence recovers.
func (r *Receiver) handle(ctx context.Context, l leased) {
	err := r.pipe.Run(ctx, ingestionOf(l.j))
	if err == nil {
		r.spool.Commit(l)
		return
	}
	if errors.Is(err, pipeline.ErrPermanent) {
		r.log.Warn("ingest record dead-lettered (permanent)", "err", err.Error())
		r.spool.deadLetter(l)
		if r.mreg != nil {
			r.mreg.CounterAdd("llmobs_ingest_wal_dead_lettered_total",
				"Ingest records dead-lettered because they can never succeed (malformed).", nil, 1)
		}
		return
	}
	// Transient: throttle, then requeue WITHOUT committing. Bounded exponential
	// backoff, interruptible by the hard shutdown so drain isn't blocked.
	r.log.Warn("ingestion pipeline error (transient; will retry, not dropped)", "err", err.Error(), "retry", l.retry)
	backoff := time.Duration(1<<min(l.retry, 5)) * 50 * time.Millisecond
	select {
	case <-time.After(backoff):
	case <-ctx.Done():
	}
	l.retry++
	r.spool.requeue(l)
}

func ingestionOf(j job) *pipeline.Ingestion {
	return &pipeline.Ingestion{
		Bearer:      j.bearer,
		Body:        j.body,
		ContentType: j.contentType,
		ReceivedAt:  j.receivedAt,
	}
}

// BeginDrain flips the receiver to draining so new OTLP requests are shed with a
// retryable 503 immediately, before the listeners are torn down. Idempotent.
func (r *Receiver) BeginDrain() { r.draining.Store(true) }

// Close releases the spool (flushing the WAL's final checkpoint and stopping its
// background checkpointer). Call after DrainAndWait.
func (r *Receiver) Close() error { return r.spool.Close() }

// DrainAndWait stops accepting new work and blocks until the in-flight queue is
// fully persisted or the deadline in ctx elapses. If the deadline wins, the
// remaining acked-but-unpersisted jobs are the one unavoidable loss window for an
// in-memory queue: they are counted (llmobs_ingest_queue_dropped_on_shutdown_total)
// and logged at error, never dropped silently. ctx SHOULD be shorter than the
// orchestrator's terminationGracePeriodSeconds so the drain runs to completion
// before SIGKILL.
func (r *Receiver) DrainAndWait(ctx context.Context) {
	r.draining.Store(true)
	r.drainOnce.Do(func() { close(r.drainC) })

	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
		// clean: the queue drained fully within the deadline
	case <-ctx.Done():
		r.hardCancel() // force workers to stop pulling; remaining stays queued
		<-done
	}

	residual := r.spool.Len()
	if residual == 0 {
		return
	}
	if r.spool.Durable() {
		// The WAL spool: undrained records are on disk and replay on next boot — not
		// lost. Record them as deferred, not dropped.
		r.log.Info("ingest drain deadline elapsed; undrained records are durable and will replay on boot",
			"deferred", residual)
		if r.mreg != nil {
			r.mreg.CounterAdd("llmobs_ingest_records_deferred_to_replay_total",
				"Acked ingest records still buffered at shutdown that are durable (WAL) and replay on boot.",
				nil, float64(residual))
		}
		return
	}
	// The in-memory spool: this is the one unavoidable loss window.
	r.log.Error("ingest queue not drained before shutdown deadline; acked spans dropped",
		"dropped", residual)
	if r.mreg != nil {
		r.mreg.CounterAdd("llmobs_ingest_queue_dropped_on_shutdown_total",
			"Acked ingest jobs dropped because the shutdown drain deadline elapsed before the queue emptied.",
			nil, float64(residual))
	}
}
