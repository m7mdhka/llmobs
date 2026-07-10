// Package pipeline is the D10 ingestion middleware chain as a real ordered
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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/normalize"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// Ingestion is the mutable context threaded through the chain.
type Ingestion struct {
	// input (set by the receiver)
	Bearer      string
	Body        []byte
	ContentType string
	ReceivedAt  time.Time

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
}

// StageConfig is a placeholder for per-stage configuration.
type StageConfig struct {
	Enabled bool
}

// Pipeline is the ordered chain.
type Pipeline struct {
	stages []Stage
}

// New builds the default chain.
func New(pool *pgxpool.Pool, store storage.TelemetryStore, reg *normalize.Registry, bus EventBus, _ Config) *Pipeline {
	return &Pipeline{stages: []Stage{
		&authenticateStage{pool: pool},
		&decodeStage{},
		&normalizeStage{reg: reg},
		&redactStage{}, // no-op (issue: kernel-ingestion redaction)
		&sampleStage{}, // no-op (issue: kernel-ingestion sampling)
		&enrichStage{}, // no-op (issue: cost derivation / price table)
		&persistStage{store: store},
		&publishStage{bus: bus}, // no-op in-proc bus (issue: Redis Streams bus)
	}}
}

// Run executes the chain in order, stopping at the first error.
func (p *Pipeline) Run(ctx context.Context, ing *Ingestion) error {
	for _, s := range p.stages {
		if err := s.Process(ctx, ing); err != nil {
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
