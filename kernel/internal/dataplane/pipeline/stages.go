package pipeline

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/normalize"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

// authenticate: resolve the bearer secret to an identity; require ingest scope.
type authenticateStage struct{ pool *pgxpool.Pool }

func (s *authenticateStage) Name() string { return "authenticate" }
func (s *authenticateStage) Process(ctx context.Context, ing *Ingestion) error {
	id, err := controlplane.Authenticate(ctx, s.pool, ing.Bearer)
	if err != nil {
		return err
	}
	if !id.HasScope("ingest") {
		return errors.New("api key lacks ingest scope")
	}
	ing.Identity = id
	return nil
}

// decode: OTLP bytes -> transport-neutral SpanInputs (protobuf or JSON).
type decodeStage struct{}

func (s *decodeStage) Name() string { return "decode" }
func (s *decodeStage) Process(_ context.Context, ing *Ingestion) error {
	ct := strings.ToLower(ing.ContentType)
	var err error
	switch {
	case strings.Contains(ct, "application/json"):
		traces, e := normalize.UnmarshalOTLPJSON(ing.Body)
		if e != nil {
			return e
		}
		ing.Spans = normalize.FromTraces(traces)
	case strings.Contains(ct, "application/x-protobuf"), ct == "":
		traces, e := normalize.UnmarshalOTLPProto(ing.Body)
		if e != nil {
			return e
		}
		ing.Spans = normalize.FromTraces(traces)
	default:
		err = errors.New("unsupported content-type: " + ing.ContentType)
	}
	return err
}

// normalize: SpanInputs -> canonical span events (upsert), keyed on span id and
// versioned by start_time (deterministic, so re-delivery is idempotent).
type normalizeStage struct{ reg *normalize.Registry }

func (s *normalizeStage) Name() string { return "normalize" }
func (s *normalizeStage) Process(_ context.Context, ing *Ingestion) error {
	ctx := normalize.Context{ProjectID: ing.Identity.ProjectID}
	for _, in := range ing.Spans {
		canonical := s.reg.Normalize(in, ctx)
		ing.Events = append(ing.Events, postgres.Event{
			Op:      postgres.OpUpsert,
			EventTS: eventTS(in),
			EventID: in.SpanID,
			Payload: canonical,
		})
	}
	return nil
}

func eventTS(in normalize.SpanInput) time.Time {
	// end_time (last-known state) if set, else start_time.
	n := in.EndUnixNano
	if n == 0 {
		n = in.StartUnixNano
	}
	return time.Unix(0, int64(n)).UTC()
}

// redact: no-op pass-through. Interface + config present.
// TODO(issue): redaction runs before persistence (D10); not built in B1.
type redactStage struct{}

func (s *redactStage) Name() string                                  { return "redact" }
func (s *redactStage) Process(_ context.Context, _ *Ingestion) error { return nil }

// sample: no-op pass-through.
// TODO(issue): per-project sampling; not built in B1.
type sampleStage struct{}

func (s *sampleStage) Name() string                                  { return "sample" }
func (s *sampleStage) Process(_ context.Context, _ *Ingestion) error { return nil }

// enrich: no-op pass-through (cost derivation needs the price table).
// TODO(issue): usage/cost derivation + pricing snapshot; returns input unchanged.
type enrichStage struct{}

func (s *enrichStage) Name() string                                  { return "enrich" }
func (s *enrichStage) Process(_ context.Context, _ *Ingestion) error { return nil }

// persist: merge-on-write each canonical span event under the row lock.
type persistStage struct{ store *postgres.Store }

func (s *persistStage) Name() string { return "persist" }
func (s *persistStage) Process(ctx context.Context, ing *Ingestion) error {
	for _, ev := range ing.Events {
		if err := s.store.PersistSpan(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

// publish: no-op in-proc bus. Interface present; Redis Streams comes with plugins.
type publishStage struct{ bus EventBus }

func (s *publishStage) Name() string { return "publish" }
func (s *publishStage) Process(ctx context.Context, ing *Ingestion) error {
	for _, ev := range ing.Events {
		_ = s.bus.Publish(ctx, "span.ingested", ev.EventID)
	}
	return nil
}
