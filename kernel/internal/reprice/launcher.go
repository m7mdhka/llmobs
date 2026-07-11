package reprice

import (
	"context"
	"log/slog"
	"sync"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// NamedScanner pairs a telemetry scanner with a stable name used to namespace its run
// state. In a dual (scale) deployment there are two — "lite" (Postgres) and "scale"
// (ClickHouse) — because a re-price must cover spans in BOTH tiers, each resuming from its
// own cursor.
type NamedScanner struct {
	Name    string
	Scanner storage.RepriceScanner
}

// Launcher starts background re-pricing runs from an admin trigger. It runs the Runner
// against EVERY configured scanner (so both profiles' spans are covered), each under a
// KeyPrefix so their resumable cursors never collide, sharing the Postgres-backed run
// state. A bounded semaphore caps concurrent runs so an admin cannot stack unbounded
// background work. The Launcher owns a root context so runs survive the triggering
// request but stop on shutdown.
type Launcher struct {
	scanners []NamedScanner
	state    storage.RepriceState
	price    Prices
	cfg      Config
	log      *slog.Logger

	rootCtx context.Context
	sem     chan struct{}

	mu      sync.Mutex
	running map[string]bool // filter run key -> in-flight, so a re-trigger is a no-op
}

// NewLauncher builds a launcher. maxConcurrent<=0 defaults to 2. rootCtx bounds the
// background runs (cancel it on shutdown).
func NewLauncher(rootCtx context.Context, scanners []NamedScanner, state storage.RepriceState, price Prices, cfg Config, maxConcurrent int, log *slog.Logger) *Launcher {
	if log == nil {
		log = slog.Default()
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	return &Launcher{
		scanners: scanners, state: state, price: price, cfg: cfg, log: log,
		rootCtx: rootCtx, sem: make(chan struct{}, maxConcurrent), running: map[string]bool{},
	}
}

// StartReprice adapts Start to the gateway's primitive-arg trigger interface (authhttp
// stays free of the storage package). Exactly one of snapshotRefID / projectID is the
// primary selector; both may be set to scope a price re-price to one tenant.
func (l *Launcher) StartReprice(snapshotRefID, projectID string) (string, bool) {
	return l.Start(storage.RepriceFilter{SnapshotRefID: snapshotRefID, ProjectID: projectID})
}

// maxInFlight caps DISTINCT in-flight run keys, so an admin firing many distinct
// (provider,model,version) filters in a tight loop cannot park an unbounded number of
// goroutines + running-map entries (the semaphore bounds concurrent EXECUTION, not the
// number of parked runs). Beyond the cap, Start refuses (started=false).
const maxInFlight = 64

// Start launches a background re-price for the filter across all scanners and returns the
// run key. It is non-blocking and idempotent: a re-trigger of an already-running filter is
// a no-op (started=false) so an impatient admin can't pile on duplicate runs. The actual
// per-span idempotency (a no-op second pass) is guaranteed by the Runner regardless.
func (l *Launcher) Start(f storage.RepriceFilter) (runKey string, started bool) {
	if f.SnapshotRefID == "" && f.ProjectID == "" {
		return "", false
	}
	runKey = RunKey(f)
	l.mu.Lock()
	if l.running[runKey] || len(l.running) >= maxInFlight {
		l.mu.Unlock()
		return runKey, false
	}
	l.running[runKey] = true
	l.mu.Unlock()

	go func() {
		defer func() {
			l.mu.Lock()
			delete(l.running, runKey)
			l.mu.Unlock()
		}()
		for _, ns := range l.scanners {
			select {
			case <-l.rootCtx.Done():
				return
			case l.sem <- struct{}{}:
			}
			cfg := l.cfg
			cfg.KeyPrefix = ns.Name + ":"
			runner := New(ns.Scanner, l.state, l.price, cfg, l.log)
			res, err := runner.Run(l.rootCtx, f)
			<-l.sem
			if err != nil {
				l.log.Error("reprice run stopped with error (resumable on re-trigger)", "scanner", ns.Name, "run", runKey, "err", err.Error())
				continue
			}
			l.log.Info("reprice run finished", "scanner", ns.Name, "run", runKey,
				"repriced", res.Repriced, "scanned", res.Scanned, "dead_lettered", res.DeadLettered, "complete", res.Complete)
		}
	}()
	return runKey, true
}
