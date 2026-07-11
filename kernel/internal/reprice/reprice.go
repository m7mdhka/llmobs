// Package reprice re-derives span cost across history against the current price table
// (Arc M / M4, 06-usage-cost.md §5). It is the payoff of M1's APPEND-ONLY versioned
// price store: because every derived cost recorded the exact price version it used
// (pricing_snapshot_ref.id), re-pricing is DETERMINISTIC — the job finds the spans
// priced against a superseded version (or, for a discount change, a project's derived
// spans), re-derives through the SAME costderive path the ingest enrich stage uses, and
// re-emits an upsert of only the cost field-groups with a fresh snapshot ref, so the
// chain stays re-derivable. (Langfuse structurally cannot do this — its prices mutate in
// place, so a re-price is best-effort against whatever the price row happens to be now.)
//
// It reuses the L5 backfill discipline exactly, because it MUTATES money across a time
// range:
//   - Bounded chunks; a (ts, project_id, id) TOTAL-ordered resumable cursor persisted
//     after every batch (same-timestamp spans never loop, #7117).
//   - A SEPARATE, generous execution budget — NOT the interactive read timeout (a short
//     timeout is exactly what broke v4's own backfill mid-run).
//   - Failure taxonomy (CLAUDE.md #12): a TRANSIENT failure (price-store or persist blip)
//     STOPS the run loud and resumable — it is NEVER converted into a per-span null,
//     because here nulling would DROP an existing cost (the ingest enrich stage nulls on
//     the same blip only because at ingest there is no cost yet to lose). A DETERMINISTIC
//     per-span anomaly (a derived span whose own model no longer resolves — structurally
//     impossible under append-only pricing) is dead-lettered and stepped over.
//   - Idempotent: a span whose re-derived cost equals its current cost is a no-op (not
//     re-emitted), so a second identical run writes nothing. Re-derivation is bit-stable
//     (pricing.Derive's sorted-key sum), so "equal" is exact.
//   - Decoupled from boot readiness: it is a background job; /readyz never waits on it.
package reprice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/costderive"
	"github.com/m7mdhka/llmobs/kernel/internal/pricing"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// Prices is the price-table surface re-derivation needs (implemented by
// postgres.PriceStore). Declared at the consumer.
type Prices interface {
	costderive.PriceLookup
	costderive.DiscountLookup
}

// Config tunes a run. Zero values fall back to sane defaults in New.
type Config struct {
	ChunkSize   int              // spans per batch (bounded); default 500
	Budget      time.Duration    // whole-run execution budget (SEPARATE from read timeout); default 30m
	MaxRetries  int              // per-batch/per-span transient retries; default 5
	BackoffBase time.Duration    // exponential backoff base; default 250ms
	Now         func() time.Time // re-price event clock (injectable for tests); default time.Now
	// KeyPrefix namespaces this runner's persisted run state, so two runners over the
	// SAME filter but DIFFERENT scanners (the lite tier vs the scale tier in a dual
	// deployment) keep independent resumable cursors instead of colliding on one key.
	KeyPrefix string
}

func (c Config) withDefaults() Config {
	if c.ChunkSize <= 0 {
		c.ChunkSize = 500
	}
	if c.Budget <= 0 {
		c.Budget = 30 * time.Minute
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = 5
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = 250 * time.Millisecond
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Runner re-prices the spans a RepriceFilter selects. Safe to run repeatedly: each run
// resumes from the persisted cursor and exits cleanly when the budget is spent.
type Runner struct {
	scan  storage.RepriceScanner // telemetry: adapter-specific (Postgres or ClickHouse)
	state storage.RepriceState   // control-plane: always Postgres
	price Prices
	cfg   Config
	log   *slog.Logger
}

func New(scan storage.RepriceScanner, state storage.RepriceState, price Prices, cfg Config, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{scan: scan, state: state, price: price, cfg: cfg.withDefaults(), log: log}
}

// Result reports what a run accomplished.
type Result struct {
	Scanned      int64 // spans examined
	Repriced     int64 // spans whose cost changed and were re-emitted
	DeadLettered int64 // spans permanently un-re-priceable (recorded, stepped over)
	Complete     bool  // every matching span scanned (no more within budget)
}

// RunKey identifies a re-pricing run so it resumes independently. A price change keys on
// the superseded snapshot-ref id; a discount change keys on the project.
func RunKey(f storage.RepriceFilter) string {
	if f.SnapshotRefID != "" {
		if f.ProjectID != "" {
			return "price:" + f.ProjectID + ":" + f.SnapshotRefID
		}
		return "price:" + f.SnapshotRefID
	}
	return "discount:" + f.ProjectID
}

// Run re-prices under a SEPARATE budget-scoped context. It returns an error only for a
// fail-loud condition (a persistent price-store/persist/state failure); budget
// exhaustion is a clean, resumable exit (Complete=false).
func (r *Runner) Run(ctx context.Context, f storage.RepriceFilter) (Result, error) {
	if f.SnapshotRefID == "" && f.ProjectID == "" {
		// An empty filter would scan every derived span in the instance — refuse it, so a
		// mis-wired trigger can never launch an unbounded, all-tenant re-price.
		return Result{}, errors.New("reprice: empty filter (need SnapshotRefID and/or ProjectID)")
	}
	runKey := r.cfg.KeyPrefix + RunKey(f)
	runCtx, cancel := context.WithTimeout(ctx, r.cfg.Budget)
	defer cancel()

	cur, repriced, scanned, err := r.state.LoadRepriceState(runCtx, runKey)
	if err != nil {
		return Result{}, err
	}
	// A non-zero starting count means we are RESUMING an interrupted run from its cursor;
	// no row means a fresh run (including a re-trigger of a previously-completed key, which
	// re-scans — see ClearRepriceState). There is no permanent "done" gate.
	res := Result{Repriced: repriced, Scanned: scanned}

	// Per-run caches: entries by canonical (provider,model), discounts by project. Fresh
	// each Run call so a run started after a price/discount edit sees the CURRENT values.
	entryCache := map[string]*pricing.Entry{}
	discountCache := map[string]float64{}

	for {
		if runCtx.Err() != nil {
			r.log.Info("reprice paused on budget (resumable)", "run", runKey, "scanned", res.Scanned, "repriced", res.Repriced)
			return res, nil
		}

		rows, next, ferr := r.fetch(runCtx, runKey, f, cur)
		if ferr != nil {
			if runCtx.Err() != nil { // budget expired mid-retry — resumable, not an error
				r.log.Info("reprice paused on budget during fetch (resumable)", "run", runKey, "scanned", res.Scanned)
				return res, nil
			}
			return res, fmt.Errorf("reprice %s fetch: %w", runKey, ferr)
		}
		if len(rows) == 0 {
			// Complete: clear the in-flight cursor so a later re-trigger of this key re-scans
			// (idempotent when nothing changed; catches a subsequent change). Best-effort —
			// a leftover row would only cause the next run to resume near the end, still
			// correct, just a little redundant work.
			if err := r.state.ClearRepriceState(runCtx, runKey); err != nil {
				return res, err
			}
			r.log.Info("reprice run complete", "run", runKey, "repriced", res.Repriced, "scanned", res.Scanned, "dead_lettered", res.DeadLettered)
			res.Complete = true
			return res, nil
		}

		for _, doc := range rows {
			ev, changed, projectID, id, derr := r.repriceSpan(runCtx, doc, entryCache, discountCache)
			if derr != nil {
				// TRANSIENT (price-store/discount blip): stop the run LOUD and resumable —
				// never null, never dead-letter, so an outage can't drop an existing cost.
				if runCtx.Err() != nil {
					r.log.Info("reprice paused on budget during derive (resumable)", "run", runKey, "scanned", res.Scanned)
					return res, nil
				}
				return res, fmt.Errorf("reprice %s: transient derive failure survived retries (price store likely unavailable); stopping — resumable: %w", runKey, derr)
			}
			res.Scanned++
			if !changed {
				continue // idempotent no-op: re-derived cost equals current
			}
			if ev == nil {
				// A derived span that no longer re-derives to any cost (its model/price
				// vanished) — structurally impossible under append-only pricing, so a
				// data-integrity anomaly. Dead-letter (recorded, auditable) and step over;
				// NEVER null the existing cost silently.
				if err := r.state.DeadLetterReprice(runCtx, runKey, projectID, id, "derived span no longer resolves to a price (append-only anomaly)"); err != nil {
					return res, err
				}
				res.DeadLettered++
				continue
			}
			if perr := r.persistWithRetry(runCtx, *ev); perr != nil {
				if runCtx.Err() != nil {
					r.log.Info("reprice paused on budget during persist (resumable)", "run", runKey, "scanned", res.Scanned)
					return res, nil
				}
				if errors.Is(perr, storage.ErrSuppressedByErasure) {
					// The span was GDPR-erased between scan and persist. Skip silently — do
					// NOT dead-letter it, which would retain an erased span's id. Re-pricing
					// must never resurrect erased data (the erasure guard already blocked it).
					continue
				}
				if isTransient(perr) {
					return res, fmt.Errorf("reprice %s: transient persist failure survived retries (store likely unavailable); stopping — resumable: %w", runKey, perr)
				}
				// A deterministic persist rejection (e.g. erasure suppression) — record and
				// step over so one span can't stall the run.
				if err := r.state.DeadLetterReprice(runCtx, runKey, projectID, id, "persist rejected (deterministic) after retries: "+perr.Error()); err != nil {
					return res, err
				}
				res.DeadLettered++
				continue
			}
			res.Repriced++
		}

		// Advance + persist the cursor AFTER the batch, so a crash mid-batch re-processes
		// it (re-emitting the same cost is idempotent) rather than skipping spans.
		cur = next
		if err := r.state.SaveRepriceState(runCtx, runKey, cur, res.Repriced, res.Scanned); err != nil {
			return res, err
		}
		r.log.Info("reprice progress", "run", runKey, "repriced", res.Repriced, "scanned", res.Scanned,
			"dead_lettered", res.DeadLettered, "cursor_ts", cur.TS.Format(time.RFC3339Nano), "cursor_id", cur.ID)
	}
}

// repriceSpan re-derives one span. Returns the cost-only upsert event to persist and
// changed=true when the re-derived cost differs from the stored cost; changed=false is an
// idempotent no-op (skip). ev==nil with changed==true signals the append-only anomaly
// (derived span no longer resolves). A non-nil error is TRANSIENT (stop the run loud).
func (r *Runner) repriceSpan(ctx context.Context, doc json.RawMessage, entryCache map[string]*pricing.Entry, discountCache map[string]float64) (ev *storage.Event, changed bool, projectID, id string, err error) {
	var m map[string]any
	if uerr := json.Unmarshal(doc, &m); uerr != nil {
		// Unreadable stored doc — not transient; treat as anomaly (ev nil, changed true).
		return nil, true, "", "", nil
	}
	projectID, _ = m["project_id"].(string)
	id, _ = m["id"].(string)

	// Snapshot the CURRENT cost before re-deriving (the derive mutates m in place). Keep the
	// old cost_details KEYS too: cost_details is a per-leaf-merged field-group with no leaf
	// tombstone, so a re-emit can overwrite or add a leaf but cannot REMOVE one.
	oldKeys := costDetailKeys(m["cost_details"])
	oldCost := costTriplet(m)

	discount, derr := r.discountFor(ctx, projectID, discountCache)
	if derr != nil {
		return nil, false, projectID, id, derr // transient
	}
	entry, rerr := costderive.DeriveSpanCost(ctx, m, r.price, discount, entryCache)
	if rerr != nil {
		return nil, false, projectID, id, rerr // transient price-resolve failure
	}
	if entry == nil {
		// No derivation happened on a span we KNOW was derived (scanned by non-empty ref):
		// its price no longer resolves. Anomaly — signal dead-letter (ev nil, changed true).
		return nil, true, projectID, id, nil
	}
	// If the new price version DROPPED a bucket the old cost had (e.g. it stops pricing
	// cache_read), the re-derived cost_details lacks that key — but the per-leaf fold would
	// leave the stale leaf in place, so sum(cost_details) would overstate total_cost AND the
	// old-vs-new compare would never converge (re-pricing forever). Explicitly zero the
	// dropped keys so the fold overwrites each stale leaf with 0 (isSet(0)=true): the
	// breakdown stays consistent with total_cost and a second run is a no-op.
	if newCD, ok := m["cost_details"].(map[string]any); ok {
		for k := range oldKeys {
			if _, present := newCD[k]; !present {
				newCD[k] = 0.0
			}
		}
	}
	newCost := costTriplet(m)
	if jsonEqualBytes(oldCost, newCost) {
		return nil, false, projectID, id, nil // unchanged — idempotent no-op
	}

	// Emit ONLY the cost field-groups so the fold updates cost and leaves every other
	// field-group at its original provenance. A fresh event_ts (re-price runs strictly
	// after ingest) makes the new cost win the latest-wins cost group.
	payload := map[string]any{
		"project_id":           projectID,
		"id":                   id,
		"cost_details":         m["cost_details"],
		"total_cost":           m["total_cost"],
		"cost_source":          "derived",
		"pricing_snapshot_ref": m["pricing_snapshot_ref"],
	}
	eventTS := r.eventTS(costderive.SpanTime(m))
	return &storage.Event{
		Op:      storage.OpUpsert,
		EventTS: eventTS,
		EventID: id + "@reprice",
		Payload: payload,
	}, true, projectID, id, nil
}

// eventTS returns the re-price event timestamp. Ingest stamps every span event at the
// span's LOGICAL time (end/start_time) keyed by SpanID, and a re-delivery shares that
// exact (event_ts, event_id) so it is idempotent and cannot change cost — meaning the
// ONLY writers of a span's cost group are ingest (at logical time) and this re-price. The
// run clock (wall-time, far past any historical span's logical time) therefore wins the
// latest-wins cost group cleanly, with no legitimate later writer to shadow. The bump
// guards the pathological future-dated span (logical time ahead of now) so the re-price
// still wins even then.
func (r *Runner) eventTS(spanTime time.Time) time.Time {
	now := r.cfg.Now().UTC()
	if !now.After(spanTime) {
		return spanTime.Add(time.Nanosecond)
	}
	return now
}

// discountFor resolves a project's discount once, caching it. A lookup error is TRANSIENT
// (returned to stop the run) — NEVER treated as "no discount", which would overcharge.
func (r *Runner) discountFor(ctx context.Context, projectID string, cache map[string]float64) (float64, error) {
	if d, ok := cache[projectID]; ok {
		return d, nil
	}
	d, _, err := r.price.GetDiscount(ctx, projectID)
	if err != nil {
		return 0, err
	}
	cache[projectID] = d
	return d, nil
}

func (r *Runner) fetch(ctx context.Context, runKey string, f storage.RepriceFilter, after storage.RepriceCursor) ([]json.RawMessage, storage.RepriceCursor, error) {
	var (
		rows []json.RawMessage
		next storage.RepriceCursor
		err  error
	)
	for attempt := 0; attempt <= r.cfg.MaxRetries; attempt++ {
		rows, next, err = r.scan.SpansForReprice(ctx, runKey, f, after, r.cfg.ChunkSize)
		if err == nil {
			return rows, next, nil
		}
		if !r.backoff(ctx, attempt) {
			break
		}
	}
	return nil, after, err
}

func (r *Runner) persistWithRetry(ctx context.Context, ev storage.Event) error {
	var err error
	for attempt := 0; attempt <= r.cfg.MaxRetries; attempt++ {
		err = r.scan.PersistSpan(ctx, ev)
		if err == nil {
			return nil
		}
		if errors.Is(err, storage.ErrSuppressedByErasure) {
			// An erased span must NOT be re-priced back into existence — treat as a clean
			// skip (deterministic), not a retryable error.
			return err
		}
		if !r.backoff(ctx, attempt) {
			break
		}
	}
	return err
}

func (r *Runner) backoff(ctx context.Context, attempt int) bool {
	d := r.cfg.BackoffBase << attempt
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// costTriplet marshals a span's (cost_details, total_cost, pricing_snapshot_ref) to a
// canonical JSON byte string for exact old-vs-new comparison. Go's json.Marshal sorts map
// keys, so equal costs always marshal identically (Derive is bit-stable → exact compare).
func costTriplet(m map[string]any) []byte {
	b, _ := json.Marshal([]any{m["cost_details"], m["total_cost"], m["pricing_snapshot_ref"]})
	return b
}

func jsonEqualBytes(a, b []byte) bool { return string(a) == string(b) }

// costDetailKeys returns the bucket key set of a stored cost_details map (empty if absent
// or not a map), used to detect buckets a new price version dropped.
func costDetailKeys(v any) map[string]struct{} {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	keys := make(map[string]struct{}, len(m))
	for k := range m {
		keys[k] = struct{}{}
	}
	return keys
}

// isTransient classifies a persist error (past its retries) as a transient backend
// problem (→ stop loud, resumable) vs a deterministic rejection (→ dead-letter). Mirrors
// the L5 backfill classifier: recognizes network/timeout/availability signals; anything
// else is deterministic. A mis-scoped deterministic error only ever stops the run LOUDLY
// (visible, never silent), so the conservative default favors liveness without data loss.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, storage.ErrSuppressedByErasure) {
		return false // an erasure tombstone is a deterministic, expected skip
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, sig := range []string{
		"connection refused", "connection reset", "broken pipe", "no route to host",
		"i/o timeout", "timeout", "eof", "unavailable", "dial ", "network is",
		"connect: ", "temporarily unavailable", "server is not ready", "too many connections",
	} {
		if strings.Contains(s, sig) {
			return true
		}
	}
	return false
}
