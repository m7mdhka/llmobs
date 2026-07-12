package ingest

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/pipeline"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/metrics"
)

// countingRunner is a fake pipeline: it counts processed jobs and can add a small
// per-job delay so work is still in flight when the drain begins. It honors ctx
// cancellation so the hard-deadline path can stop it mid-drain.
type countingRunner struct {
	mu        sync.Mutex
	processed int
	delay     time.Duration
	block     chan struct{} // if non-nil, Run blocks on it (until closed or ctx done)
}

func (c *countingRunner) Run(ctx context.Context, _ *pipeline.Ingestion) error {
	if c.block != nil {
		select {
		case <-c.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.mu.Lock()
	c.processed++
	c.mu.Unlock()
	return nil
}

func (c *countingRunner) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.processed
}

func enqueueN(r *Receiver, n int) {
	for i := 0; i < n; i++ {
		_ = r.spool.Append(job{bearer: "k", contentType: "application/json", body: []byte("{}"), receivedAt: time.Now()})
	}
}

// metricValue scrapes the registry exposition and returns the (unlabelled) value
// of a metric, or -1 if absent.
func metricValue(reg *metrics.Registry, name string) float64 {
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, name+" ") {
			if v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, name+" ")), 64); err == nil {
				return v
			}
		}
	}
	return -1
}

// TestDrainPersistsQueuedJobsOnShutdown is the drain structural proof: fill the
// queue, simulate SIGTERM by cancelling the context passed to Start (the old code
// killed workers on this and dropped the queue), then DrainAndWait with a generous
// deadline — every acked job must be processed and none counted as dropped.
func TestDrainPersistsQueuedJobsOnShutdown(t *testing.T) {
	const n = 20
	reg := metrics.New()
	runner := &countingRunner{delay: 2 * time.Millisecond}
	r := NewReceiver(runner, slog.New(slog.NewTextHandler(io.Discard, nil)), 64, 3, reg)

	enqueueN(r, n)

	// Simulate a shutdown signal on the context Start receives; workers must NOT die.
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	cancel()

	drainCtx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dcancel()
	r.DrainAndWait(drainCtx)

	if got := runner.count(); got != n {
		t.Fatalf("drain lost acked jobs: processed %d of %d", got, n)
	}
	if r.QueueLen() != 0 {
		t.Fatalf("queue not empty after clean drain: %d remain", r.QueueLen())
	}
	if v := metricValue(reg, "llmobs_ingest_queue_dropped_on_shutdown_total"); v > 0 {
		t.Fatalf("clean drain must not report drops, got %v", v)
	}
}

// TestDrainDeadlineCountsDrops is the drain deadline proof: when work cannot drain in
// time, DrainAndWait must return (not block forever) and count the residual as
// dropped-on-shutdown rather than losing it silently.
func TestDrainDeadlineCountsDrops(t *testing.T) {
	const n = 10
	reg := metrics.New()
	// Runner blocks forever (until ctx cancels), so the queue cannot drain.
	runner := &countingRunner{block: make(chan struct{})}
	r := NewReceiver(runner, slog.New(slog.NewTextHandler(io.Discard, nil)), 32, 1, reg)

	enqueueN(r, n)
	r.Start(context.Background())

	drainCtx, dcancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer dcancel()
	r.DrainAndWait(drainCtx) // must return within ~the deadline, not hang

	dropped := metricValue(reg, "llmobs_ingest_queue_dropped_on_shutdown_total")
	if dropped <= 0 {
		t.Fatalf("deadline path must count drops, got %v", dropped)
	}
	// Everything that didn't process is accounted as dropped (± the one job the
	// single worker was blocked on when the deadline fired).
	if int(dropped) > n || int(dropped) < n-1 {
		t.Fatalf("dropped=%v out of range for n=%d", dropped, n)
	}
}
