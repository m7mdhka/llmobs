// Package pipeline is the ingestion middleware chain as a real ordered
// composition with per-stage interfaces:
//
//	authenticate -> decode -> normalize -> redact -> sample -> enrich -> persist -> publish
//
// authenticate/decode/normalize/persist are implemented for real; redact, sample,
// enrich, and publish are no-op pass-throughs with correct interfaces and config
// plumbing (tracked issues linked in their files) so retrofitting configurability
// later is not needed. The chain is per-project configurable in structure now
// even though only defaults exist.
package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/ingesthealth"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/normalize"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/redact"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/metrics"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// Ingestion is the mutable context threaded through the chain.
type Ingestion struct {
	// input (set by the receiver)
	Bearer      string
	Body        []byte
	ContentType string
	ReceivedAt  time.Time
	// Source, when set, is stamped onto every canonical span as llmobs.source by
	// the normalize stage. The kernel sets it for plugin ingest ("plugin:{id}") so
	// a plugin cannot forge a different source — the body's own source is
	// overwritten. Empty for OTLP (unstamped).
	Source string

	// derived
	Identity controlplane.Identity
	Spans    []normalize.SpanInput
	Events   []storage.Event
}

// Stage is one step in the chain.
type Stage interface {
	Name() string
	Process(ctx context.Context, ing *Ingestion) error
}

// Config is the per-project chain configuration. Only defaults exist in v1alpha1;
// the structure is present so per-project overrides slot in without a retrofit.
type Config struct {
	// Redact, Sample, Enrich, Publish carry per-stage config (empty defaults).
	Redact  StageConfig
	Sample  StageConfig
	Enrich  StageConfig
	Publish StageConfig
	// SkewThreshold: producer-vs-receive time drift beyond this stamps
	// llmobs.dq.clock_skew. Zero disables detection.
	SkewThreshold time.Duration
	// Metrics is the shared registry; nil disables instrumentation.
	Metrics *metrics.Registry
	// Signal is the shared persist-health gate; nil disables health-driven
	// readiness + backpressure (the persist stage still runs).
	Signal *ingesthealth.Signal
	// Prices resolves the price table for the enrich stage's cost derivation. Nil
	// makes enrich a no-op pass-through (cost simply absent, never wrong).
	Prices PriceResolver
	// Logger for fail-soft stage diagnostics (enrich price-lookup failures); nil = quiet.
	Logger *slog.Logger
	// RedactPresets/RedactCustom configure built-in payload redaction. Empty
	// presets + no custom rules disables it. This is a global default today; the
	// redact stage is structured for per-project resolution (the seam is present,
	// the per-project config STORE is the noted follow-up).
	RedactPresets []string
	RedactCustom  []redact.CustomRule
}

// StageConfig is a placeholder for per-stage configuration.
type StageConfig struct {
	Enabled bool
}

// Pipeline is the ordered chain.
type Pipeline struct {
	stages  []Stage
	metrics *metrics.Registry
}

// New builds the default chain.
func New(pool *pgxpool.Pool, store storage.TelemetryStore, reg *normalize.Registry, bus EventBus, cfg Config) *Pipeline {
	// Build the redactor once from the (global) config; the resolver returns it for
	// any project. Per-project overrides slot in by replacing this resolver with a
	// config-store lookup — the stage does not change.
	var resolve func(string) *redact.Redactor
	if len(cfg.RedactPresets) > 0 || len(cfg.RedactCustom) > 0 {
		red := redact.New(cfg.RedactPresets, cfg.RedactCustom)
		resolve = func(string) *redact.Redactor { return red }
	}
	return &Pipeline{
		metrics: cfg.Metrics,
		stages: []Stage{
			&authenticateStage{pool: pool},
			&decodeStage{},
			&normalizeStage{reg: reg, skewThreshold: cfg.SkewThreshold},
			&redactStage{resolve: resolve},
			&sampleStage{},                                    // no-op (issue: kernel-ingestion sampling)
			&enrichStage{prices: cfg.Prices, log: cfg.Logger}, // cost derivation
			&persistStage{store: store, metrics: cfg.Metrics, signal: cfg.Signal},
			&publishStage{bus: bus}, // no-op in-proc bus (issue: Redis Streams bus)
		},
	}
}

// Run executes the chain in order, stopping at the first error. Per-stage latency
// is recorded when metrics are enabled.
func (p *Pipeline) Run(ctx context.Context, ing *Ingestion) error {
	return p.run(ctx, ing, false)
}

// RunPreauth runs the pipeline for an ALREADY-AUTHENTICATED ingestion — the plugin
// ingest capability. The caller sets ing.Identity (project from the double
// token) and ing.Source ("plugin:{id}", stamped by the kernel), and the
// authenticate stage is skipped. Everything else — decode, normalize, redact,
// sample, enrich, persist, publish — runs identically, so plugin-pushed spans go
// through the SAME validation/merge/dq as OTLP, land ONLY in the caller's project,
// and carry the kernel-stamped source. A plugin cannot bypass the pipeline, forge
// its source, or write outside its project.
func (p *Pipeline) RunPreauth(ctx context.Context, ing *Ingestion) error {
	if ing.Identity.ProjectID == "" {
		return errors.New("pipeline: RunPreauth requires a resolved identity")
	}
	return p.run(ctx, ing, true)
}

func (p *Pipeline) run(ctx context.Context, ing *Ingestion, skipAuth bool) error {
	for _, s := range p.stages {
		if skipAuth && s.Name() == "authenticate" {
			continue
		}
		start := time.Now()
		err := s.Process(ctx, ing)
		if p.metrics != nil {
			p.metrics.Observe("llmobs_pipeline_stage_seconds", "Pipeline stage latency (seconds).",
				map[string]string{"stage": s.Name()}, time.Since(start).Seconds())
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Stages returns the ordered stage names (for logging / introspection).
func (p *Pipeline) Stages() []string {
	names := make([]string, len(p.stages))
	for i, s := range p.stages {
		names[i] = s.Name()
	}
	return names
}
