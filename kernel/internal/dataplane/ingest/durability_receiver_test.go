package ingest

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/platform/metrics"
)

// walReceiver builds a receiver backed by a WAL spool at a temp dir.
func walReceiver(t *testing.T, capacity int, pipe Runner, reg *metrics.Registry) (*Receiver, *walSpool) {
	t.Helper()
	sp, err := newWALSpool(t.TempDir(), capacity, 0, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sp.Close() })
	r := NewReceiver(pipe, slog.New(slog.NewTextHandler(io.Discard, nil)), capacity, 1, reg)
	r.SetSpool(sp)
	return r, sp
}

// TestG2BackpressureViaSpoolFill proves the WAL spool preserves the G2 contract:
// once the durable spool is full, new OTLP requests are shed with a retryable 503
// and a Retry-After header (the client retries into the idempotent merge).
func TestG2BackpressureViaSpoolFill(t *testing.T) {
	// Never-processing runner (block forever) + no workers started, so appended
	// records accumulate in the durable spool and it fills.
	r, _ := walReceiver(t, 4, &countingRunner{block: make(chan struct{})}, nil)

	post := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		r.Handler().ServeHTTP(w, req)
		return w
	}
	var acked, shed503 int
	var lastShed *httptest.ResponseRecorder
	for i := 0; i < 8; i++ {
		w := post()
		switch w.Code {
		case http.StatusOK:
			acked++
		case http.StatusServiceUnavailable:
			shed503++
			lastShed = w
		default:
			t.Fatalf("unexpected status %d", w.Code)
		}
	}
	if acked == 0 {
		t.Fatal("expected some acks before the spool filled")
	}
	if shed503 == 0 {
		t.Fatal("a filled durable spool must shed with 503 (G2)")
	}
	if ra := lastShed.Header().Get("Retry-After"); ra != "1" {
		t.Fatalf("shed must carry Retry-After: 1, got %q", ra)
	}
}

// TestG1UndrainedRecordsAreDeferredNotDropped proves the WAL spool turns the
// SIGKILL loss window into a replay: records still in-flight at the drain deadline
// are durable, so they are counted as DEFERRED (replay on boot), not DROPPED.
func TestG1UndrainedRecordsAreDeferredNotDropped(t *testing.T) {
	reg := metrics.New()
	block := make(chan struct{})
	r, sp := walReceiver(t, 8, &countingRunner{block: block}, reg)

	// Append two durable records; workers will lease them but block in Run, so they
	// stay in-flight (uncommitted) through the drain.
	for i := 0; i < 2; i++ {
		if err := sp.Append(mkjob("held")); err != nil {
			t.Fatal(err)
		}
	}
	r.Start(context.Background())
	// Give a worker a moment to lease into the blocking Run.
	deadlineWait(func() bool { return sp.Len() == 2 }, time.Second)

	// Drain with an already-expired deadline: nothing can complete, records remain.
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	r.DrainAndWait(ctx)
	close(block)

	if v := metricValue(reg, "llmobs_ingest_records_deferred_to_replay_total"); v <= 0 {
		t.Fatalf("undrained durable records must be counted as deferred, got %v", v)
	}
	if v := metricValue(reg, "llmobs_ingest_queue_dropped_on_shutdown_total"); v > 0 {
		t.Fatalf("durable records must NOT be counted as dropped, got %v", v)
	}
}

func deadlineWait(cond func() bool, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
}
