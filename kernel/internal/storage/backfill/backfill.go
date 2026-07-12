// Package backfill copies settled lite (Postgres) rows into the scale (ClickHouse)
// adapter in the background. It is a CONVENIENCE, not a correctness requirement: the
// permanent dual-read layer already makes historical lite data readable through the
// unified Query API, so a store that is 0%, 50%, or 100%
// backfilled is equally correct to a reader. Backfill just moves cold data onto the
// scale engine over time.
//
// Design constraints (each maps to a real migration failure mode seen in the wild):
//   - Resumable via a (ts, project_id, id) TOTAL-ordered cursor persisted after every
//     batch. Strict tuple advance means a cluster of same-timestamp rows never loops
//     forever re-reading its own timestamp.
//   - A SEPARATE, generous execution budget — NOT the interactive read timeout. A
//     short query timeout is exactly what broke v4's own backfill mid-run.
//   - Bounded chunks, per-batch retry with backoff, visible progress.
//   - Fail loud, never silent-hang: a persistent lite-read failure stops the run with
//     an error; a per-row deterministic failure dead-letters (recorded, auditable) so
//     the run still makes progress (the permanent-vs-transient failure taxonomy:
//     permanent failures dead-letter, transient failures retry and are never dropped).
//   - Decoupled from boot readiness: the runner is a background task; /readyz never
//     waits on it, and a partial backfill is fully correct via dual-read.
package backfill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

// Source is the lite side: the settled-row scan + resumable progress state. The
// concrete *postgres.Store satisfies it; the narrow interface keeps the runner
// testable.
type Source interface {
	BackfillSpans(ctx context.Context, after postgres.BackfillCursor, limit int) ([]json.RawMessage, postgres.BackfillCursor, error)
	BackfillScores(ctx context.Context, after postgres.BackfillCursor, limit int) ([]json.RawMessage, postgres.BackfillCursor, error)
	LoadBackfillState(ctx context.Context, kind string) (postgres.BackfillCursor, bool, int64, error)
	SaveBackfillState(ctx context.Context, kind string, cur postgres.BackfillCursor, done bool, migrated int64) error
	DeadLetterBackfill(ctx context.Context, kind, projectID, id, reason string) error
}

// Sink is the scale side: the same write methods the pipeline uses. Replaying a
// settled doc as an upsert event re-folds to itself, seeding scale with lite's state.
type Sink interface {
	PersistSpan(ctx context.Context, ev storage.Event) error
	PersistScore(ctx context.Context, ev storage.Event) error
}

// Config tunes the run. Zero values fall back to sane defaults in New.
type Config struct {
	ChunkSize   int           // rows per batch (bounded); default 500
	Budget      time.Duration // whole-run execution budget (SEPARATE from read timeout); default 30m
	MaxRetries  int           // per-batch/per-row transient retries; default 5
	BackoffBase time.Duration // exponential backoff base; default 250ms
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
	return c
}

// Runner migrates lite→scale. It is safe to run repeatedly: each run resumes from the
// persisted cursor and exits cleanly when the budget is spent (the next run continues).
type Runner struct {
	src Source
	dst Sink
	cfg Config
	log *slog.Logger
}

func New(src Source, dst Sink, cfg Config, log *slog.Logger) *Runner {
	return &Runner{src: src, dst: dst, cfg: cfg.withDefaults(), log: log}
}

// Result reports what a run accomplished (for tests, metrics, and the CLI).
type Result struct {
	SpansMigrated  int64
	ScoresMigrated int64
	DeadLettered   int64
	Complete       bool // both kinds fully done (no more rows within budget)
}

// Run migrates spans then scores under a SEPARATE budget-scoped context. It returns
// an error only for a fail-loud condition (a persistent lite-read or state-save
// failure); budget exhaustion is a clean, resumable exit (Complete=false).
func (r *Runner) Run(ctx context.Context) (Result, error) {
	runCtx, cancel := context.WithTimeout(ctx, r.cfg.Budget)
	defer cancel()

	var res Result
	spans, sComplete, err := r.migrateKind(runCtx, "spans")
	res.SpansMigrated, res.DeadLettered = spans.migrated, res.DeadLettered+spans.dead
	if err != nil {
		return res, err
	}
	scores, cComplete, err := r.migrateKind(runCtx, "scores")
	res.ScoresMigrated, res.DeadLettered = scores.migrated, res.DeadLettered+scores.dead
	if err != nil {
		return res, err
	}
	res.Complete = sComplete && cComplete
	return res, nil
}

type kindStats struct {
	migrated int64
	dead     int64
}

// migrateKind drains one kind. Returns whether it fully completed (no rows left) or
// stopped on budget (resumable). A fail-loud error stops the whole run.
func (r *Runner) migrateKind(ctx context.Context, kind string) (kindStats, bool, error) {
	cur, done, migrated, err := r.src.LoadBackfillState(ctx, kind)
	if err != nil {
		return kindStats{}, false, err
	}
	stats := kindStats{migrated: migrated}
	if done {
		r.log.Info("backfill kind already complete", "kind", kind, "migrated", migrated)
		return stats, true, nil
	}

	for {
		// Budget check FIRST: a clean, resumable exit — never a silent hang. The cursor
		// is already durable from the previous batch's save, so the next run resumes here.
		if ctx.Err() != nil {
			r.log.Info("backfill paused on budget (resumable)", "kind", kind, "migrated", stats.migrated)
			return stats, false, nil
		}

		rows, next, ferr := r.fetch(ctx, kind, cur)
		if ferr != nil {
			// A persistent lite-read failure after retries is fail-loud: stop the run so
			// an operator sees it. The cursor is unchanged, so a later run resumes cleanly.
			if ctx.Err() != nil { // budget expired mid-retry — resumable, not an error
				r.log.Info("backfill paused on budget during fetch (resumable)", "kind", kind, "migrated", stats.migrated)
				return stats, false, nil
			}
			return stats, false, fmt.Errorf("backfill %s fetch: %w", kind, ferr)
		}
		if len(rows) == 0 {
			if err := r.src.SaveBackfillState(ctx, kind, cur, true, stats.migrated); err != nil {
				return stats, false, err
			}
			r.log.Info("backfill kind complete", "kind", kind, "migrated", stats.migrated)
			return stats, true, nil
		}

		for _, doc := range rows {
			projectID, id, ev, ok := syntheticEvent(kind, doc)
			if !ok {
				// A settled doc we can't turn into an event (missing id / malformed) is a
				// PERMANENT per-row failure: dead-letter and skip, never retry, never drop.
				if err := r.src.DeadLetterBackfill(ctx, kind, projectID, id, "malformed settled doc: missing id or unparseable"); err != nil {
					return stats, false, err
				}
				stats.dead++
				continue
			}
			if perr := r.persistWithRetry(ctx, kind, ev); perr != nil {
				if ctx.Err() != nil { // budget expired mid-retry — stop cleanly, cursor not advanced past this batch
					r.log.Info("backfill paused on budget during persist (resumable)", "kind", kind, "migrated", stats.migrated)
					return stats, false, nil
				}
				// The permanent-vs-transient failure taxonomy, classified at the persist error:
				//   - TRANSIENT (backend down/unreachable/timeout, survived retries): STOP
				//     the run LOUD and resumable — never dead-letter, so an outage can't be
				//     converted into dropped rows. The cursor is unchanged; a later run
				//     resumes and re-persists once the backend recovers.
				//   - DETERMINISTIC (a per-row rejection): dead-letter (recorded + redrivable)
				//     and CONTINUE, so the cursor advances past it and one bad row — or a
				//     contiguous cluster of them — can never permanently stall the migration.
				if isTransient(perr) {
					return stats, false, fmt.Errorf(
						"backfill %s: transient persist failure survived retries (scale backend likely unavailable); stopping — resumable on next run: %w",
						kind, perr)
				}
				if err := r.src.DeadLetterBackfill(ctx, kind, projectID, id, "persist rejected (deterministic) after retries: "+perr.Error()); err != nil {
					return stats, false, err
				}
				stats.dead++
				continue
			}
			stats.migrated++
		}

		// Advance + persist the cursor AFTER the batch is fully processed, so a crash
		// mid-batch re-processes the batch (idempotent upserts) rather than skipping it.
		cur = next
		if err := r.src.SaveBackfillState(ctx, kind, cur, false, stats.migrated); err != nil {
			return stats, false, err
		}
		r.log.Info("backfill progress", "kind", kind, "migrated", stats.migrated, "dead_lettered", stats.dead,
			"cursor_ts", cur.TS.Format(time.RFC3339Nano), "cursor_id", cur.ID)
	}
}

// fetch reads one chunk with bounded retry + exponential backoff (transient lite-read
// blips). The read runs on the budget-scoped ctx — its own generous window, never the
// interactive statement timeout.
func (r *Runner) fetch(ctx context.Context, kind string, after postgres.BackfillCursor) ([]json.RawMessage, postgres.BackfillCursor, error) {
	var (
		rows []json.RawMessage
		next postgres.BackfillCursor
		err  error
	)
	for attempt := 0; attempt <= r.cfg.MaxRetries; attempt++ {
		if kind == "scores" {
			rows, next, err = r.src.BackfillScores(ctx, after, r.cfg.ChunkSize)
		} else {
			rows, next, err = r.src.BackfillSpans(ctx, after, r.cfg.ChunkSize)
		}
		if err == nil {
			return rows, next, nil
		}
		if !r.backoff(ctx, attempt) {
			break
		}
	}
	return nil, after, err
}

// persistWithRetry writes one event into scale with bounded retry + backoff.
func (r *Runner) persistWithRetry(ctx context.Context, kind string, ev storage.Event) error {
	var err error
	for attempt := 0; attempt <= r.cfg.MaxRetries; attempt++ {
		if kind == "scores" {
			err = r.dst.PersistScore(ctx, ev)
		} else {
			err = r.dst.PersistSpan(ctx, ev)
		}
		if err == nil {
			return nil
		}
		if !r.backoff(ctx, attempt) {
			break
		}
	}
	return err
}

// backoff sleeps base*2^attempt, honoring ctx cancellation. Returns false if ctx is
// done (caller stops retrying) — so a spent budget never hangs the run.
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

// isTransient classifies a persist error (already past its retries) as a transient
// backend problem (→ stop the run loud, resumable) vs a deterministic per-row
// rejection (→ dead-letter, continue). It recognizes network/timeout/availability
// signals; anything else is treated as deterministic so a bad row is recorded and the
// run makes forward progress (a dead-letter is redrivable, never a silent drop). A
// deterministic error mis-scoped as transient only ever stops the run LOUDLY — visible,
// never silent — so the conservative default favors liveness without losing data.
func isTransient(err error) bool {
	if err == nil {
		return false
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

// syntheticEvent turns a settled doc into an upsert event (re-folding a settled doc
// yields itself). Returns project_id + id for dead-lettering even when the event is
// unbuildable. ok=false means the doc has no id / is unparseable (a permanent skip).
func syntheticEvent(kind string, doc json.RawMessage) (projectID, id string, ev storage.Event, ok bool) {
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		return "", "", storage.Event{}, false
	}
	projectID, _ = m["project_id"].(string)
	id, _ = m["id"].(string)
	if id == "" {
		return projectID, id, storage.Event{}, false
	}
	return projectID, id, storage.Event{
		Op:      storage.OpUpsert,
		EventTS: anchorTime(kind, m),
		EventID: id + "@backfill",
		Payload: m,
	}, true
}

// anchorTime reads the entity's ordering anchor (span start_time, score timestamp)
// so a historical replay never wins a field over a real post-cutover event.
func anchorTime(kind string, m map[string]any) time.Time {
	keys := []string{"start_time"}
	if kind == "scores" {
		keys = []string{"timestamp"}
	}
	for _, k := range keys {
		if s, ok := m[k].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				return t.UTC()
			}
		}
	}
	return time.Time{}
}
