package platform

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/ingesthealth"
)

// Health serves liveness (/healthz) and readiness (/readyz). Readiness reports
// ready only when the replica can actually serve: persistence is healthy AND
// the database is reachable. When persistence is failing, readiness goes
// not-ready so the orchestrator stops routing new traffic here — in multi-replica
// a healthy peer absorbs it; single-replica, this is correct backpressure (clients
// retry into the idempotent merge rather than getting a false ack).
type Health struct {
	pool   *pgxpool.Pool
	signal *ingesthealth.Signal
}

// NewHealth wires readiness to the pool and (optionally) the persist-health
// signal. A nil signal disables the persist-health gate (pool ping only).
func NewHealth(pool *pgxpool.Pool, signal *ingesthealth.Signal) *Health {
	return &Health{pool: pool, signal: signal}
}

// Register wires the health endpoints onto a mux.
func (h *Health) Register(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		// Persist-health gate first (cheap, no I/O): a connectable-but-unwritable DB
		// (disk full, failover) still pings OK, so the write signal must be checked.
		if h.signal != nil && !h.signal.Healthy() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready: persistence unhealthy"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.pool.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
}
