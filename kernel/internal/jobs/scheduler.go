package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"
)

// JobSpec is one declared job on a plugin.
type JobSpec struct {
	Name        string
	Schedule    string // cron / @every; empty => on-demand only
	Path        string // endpoint on the plugin backend
	MaxAttempts int
}

// PluginJobs is a running plugin's jobs + the grant the runner scopes the system
// assertion to (the plugin's own capabilities/permissions — never wider).
type PluginJobs struct {
	PluginID    string
	BackendURL  string
	Permissions []string // canonical perms => the system assertion's scopes
	Running     bool
	Jobs        []JobSpec
}

// JobSource supplies the currently-installed plugins' jobs (the supervisor).
type JobSource interface {
	PluginJobs() []PluginJobs
}

// Scheduler evaluates schedules and dispatches job runs. Tick is called on the
// leader's reconcile cadence (advisory-lock leader-elected, reused from the
// supervisor loop). Job invocations run in their own goroutines so a long job does
// not block the tick — and a job already running is not re-triggered.
type Scheduler struct {
	source         JobSource
	runner         *Runner
	store          Store
	log            *slog.Logger
	defaultProject string
	now            func() time.Time
}

func New(source JobSource, runner *Runner, store Store, defaultProject string, log *slog.Logger, now func() time.Time) *Scheduler {
	if now == nil {
		now = time.Now
	}
	return &Scheduler{source: source, runner: runner, store: store, defaultProject: defaultProject, log: log, now: now}
}

// Tick evaluates every running plugin's scheduled jobs and dispatches those due.
func (s *Scheduler) Tick(ctx context.Context) {
	now := s.now()
	for _, pj := range s.source.PluginJobs() {
		if !pj.Running {
			continue
		}
		for _, j := range pj.Jobs {
			if j.Schedule == "" {
				continue // on-demand only
			}
			last, err := s.store.LastRun(ctx, pj.PluginID, j.Name)
			if err != nil {
				continue
			}
			due, err := Due(j.Schedule, now, last)
			if err != nil {
				s.log.Warn("bad job schedule", "plugin_id", pj.PluginID, "job", j.Name, "err", err.Error())
				continue
			}
			if !due {
				continue
			}
			running, err := s.store.IsRunning(ctx, pj.PluginID, j.Name)
			if err != nil || running {
				continue // long-running-job awareness: don't double-trigger
			}
			s.dispatch(ctx, pj, j, "schedule", SystemActor(pj.PluginID, j.Name))
		}
	}
}

// Trigger runs a job on demand (operator/actor-initiated). Returns the run id.
func (s *Scheduler) Trigger(ctx context.Context, pluginID, job, actor string) (string, error) {
	for _, pj := range s.source.PluginJobs() {
		if pj.PluginID != pluginID || !pj.Running {
			continue
		}
		for _, j := range pj.Jobs {
			if j.Name != job {
				continue
			}
			if running, _ := s.store.IsRunning(ctx, pluginID, job); running {
				return "", errAlreadyRunning
			}
			return s.dispatch(ctx, pj, j, "on_demand", actor), nil
		}
	}
	return "", errNotFound
}

func (s *Scheduler) dispatch(ctx context.Context, pj PluginJobs, j JobSpec, trigger, actor string) string {
	runID := randomID()
	run := Run{ID: runID, PluginID: pj.PluginID, Job: j.Name, Trigger: trigger, Actor: actor, Status: "running", StartedAt: s.now().UTC()}
	if err := s.store.StartRun(ctx, run); err != nil {
		s.log.Warn("job start-run failed", "plugin_id", pj.PluginID, "job", j.Name, "err", err.Error())
		return ""
	}
	go func() {
		// The system assertion is scoped to the plugin's OWN grant on its project —
		// never more. For on-demand runs by a user actor, the same bounded
		// grant applies (a trigger cannot elevate beyond the plugin's grant).
		asr, err := s.runner.systemAssertion(pj.PluginID, s.defaultProject, j.Name, pj.Permissions)
		if err != nil {
			_ = s.store.FinishRun(context.Background(), runID, "failed", "assertion mint failed", 0)
			return
		}
		attempts, err := s.runner.invoke(ctx, pj.BackendURL, j.Path, asr, j.MaxAttempts)
		if err != nil {
			s.log.Warn("job failed", "plugin_id", pj.PluginID, "job", j.Name, "actor", actor, "attempts", attempts, "err", err.Error())
			_ = s.store.FinishRun(context.Background(), runID, "failed", err.Error(), attempts)
			return
		}
		s.log.Info("job succeeded", "plugin_id", pj.PluginID, "job", j.Name, "actor", actor, "attempts", attempts)
		_ = s.store.FinishRun(context.Background(), runID, "succeeded", "", attempts)
	}()
	return runID
}

// Runs returns a plugin's recent job runs (the audit trail).
func (s *Scheduler) Runs(ctx context.Context, pluginID string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.store.RecentRuns(ctx, pluginID, limit)
}

func randomID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "run_" + hex.EncodeToString(b[:])
}
