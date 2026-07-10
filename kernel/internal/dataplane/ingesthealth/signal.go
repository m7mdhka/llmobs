// Package ingesthealth is the shared persist-health signal that lets the ingest
// backpressure envelope and the readiness probe agree on one fact: can the
// storage adapter currently accept writes? It is a tiny, dependency-free, lock-
// free value passed explicitly to the persist stage (writer), the OTLP receivers
// (backpressure reader), and the /readyz handler (readiness reader) — no global
// state, no import cycle (it imports nothing kernel-internal).
//
// Health flips unhealthy only after a run of consecutive persist failures (not a
// single transient error) and recovers on the first success, so it does not flap.
// Context cancellation / deadline errors (shutdown, request timeout) are NOT
// counted as storage-health failures.
package ingesthealth

import (
	"context"
	"errors"
	"sync/atomic"
)

// Signal is a concurrency-safe persist-health flag.
type Signal struct {
	healthy    atomic.Bool
	consecFail atomic.Int64
	threshold  int64
}

// New returns a Signal that flips unhealthy after failThreshold consecutive
// persist failures. A threshold <= 0 defaults to 1 (flip on the first real
// failure). The signal starts healthy.
func New(failThreshold int) *Signal {
	if failThreshold <= 0 {
		failThreshold = 1
	}
	s := &Signal{threshold: int64(failThreshold)}
	s.healthy.Store(true)
	return s
}

// RecordPersist folds one persist outcome into the health state. A nil error
// resets the failure run and marks healthy; a real error advances the run and,
// once it reaches the threshold, marks unhealthy. Context cancellation/deadline
// errors are ignored — they signal shutdown or timeout, not storage health.
func (s *Signal) RecordPersist(err error) {
	if s == nil {
		return
	}
	if err == nil {
		s.consecFail.Store(0)
		s.healthy.Store(true)
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if s.consecFail.Add(1) >= s.threshold {
		s.healthy.Store(false)
	}
}

// Healthy reports whether the storage adapter is currently accepting writes.
func (s *Signal) Healthy() bool {
	if s == nil {
		return true
	}
	return s.healthy.Load()
}
