package pipeline

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/normalize"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/redact"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/metrics"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
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
type normalizeStage struct {
	reg           *normalize.Registry
	skewThreshold time.Duration
}

func (s *normalizeStage) Name() string { return "normalize" }
func (s *normalizeStage) Process(_ context.Context, ing *Ingestion) error {
	ctx := normalize.Context{ProjectID: ing.Identity.ProjectID}
	for _, in := range ing.Spans {
		canonical := s.reg.Normalize(in, ctx)
		et := eventTS(in)
		stampClockSkew(canonical, et, ing.ReceivedAt, s.skewThreshold)
		ing.Events = append(ing.Events, storage.Event{
			Op:      storage.OpUpsert,
			EventTS: et,
			EventID: in.SpanID,
			Payload: canonical,
		})
	}
	return nil
}

// stampClockSkew records llmobs.dq.clock_skew when producer time (event_ts)
// deviates from receive time beyond the threshold — the edge-fleet reality
// (Story 30). Detection only: producer time still wins the merge (see ADR-0022).
func stampClockSkew(canonical map[string]any, eventTS, receivedAt time.Time, threshold time.Duration) {
	if receivedAt.IsZero() || threshold <= 0 {
		return
	}
	delta := eventTS.Sub(receivedAt).Seconds()
	if delta < 0 {
		delta = -delta
	}
	if delta <= threshold.Seconds() {
		return
	}
	attrs, ok := canonical["attributes"].(map[string]any)
	if !ok {
		attrs = map[string]any{}
		canonical["attributes"] = attrs
	}
	attrs["llmobs.dq.clock_skew"] = eventTS.Sub(receivedAt).Seconds()
}

func eventTS(in normalize.SpanInput) time.Time {
	// end_time (last-known state) if set, else start_time.
	n := in.EndUnixNano
	if n == 0 {
		n = in.StartUnixNano
	}
	return time.Unix(0, int64(n)).UTC()
}

// redact: scrub PII/secret patterns from payload fields BEFORE persist (#11).
// Scope: input, output, and span-event attribute VALUES — never keys, never
// promoted fields. Redaction is observable: llmobs.dq.redacted carries per-rule
// counts. The redactor is resolved per event (today a global default; the
// per-project seam is `resolve`).
type redactStage struct {
	resolve func(projectID string) *redact.Redactor
}

func (s *redactStage) Name() string { return "redact" }
func (s *redactStage) Process(_ context.Context, ing *Ingestion) error {
	if s.resolve == nil {
		return nil
	}
	r := s.resolve(ing.Identity.ProjectID)
	if r == nil || !r.Enabled() {
		return nil
	}
	for _, ev := range ing.Events {
		redactEvent(r, ev.Payload)
	}
	return nil
}

// payloadKeys are the span fields whose VALUES may carry prompt/response content.
var payloadKeys = []string{"input", "output"}

func redactEvent(r *redact.Redactor, payload map[string]any) {
	counts := map[string]int{}
	for _, k := range payloadKeys {
		if v, ok := payload[k]; ok {
			red, c := r.RedactValue(v)
			payload[k] = red
			for rk, rv := range c {
				counts[rk] += rv
			}
		}
	}
	// Span-event attribute values (message content lives here).
	if events, ok := payload["events"].([]any); ok {
		for _, e := range events {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if attrs, ok := em["attributes"].(map[string]any); ok {
				red, c := r.RedactValue(attrs)
				em["attributes"] = red
				for rk, rv := range c {
					counts[rk] += rv
				}
			}
		}
	}
	if redact.TotalCount(counts) > 0 {
		attrs, ok := payload["attributes"].(map[string]any)
		if !ok {
			attrs = map[string]any{}
			payload["attributes"] = attrs
		}
		attrs["llmobs.dq.redacted"] = redact.CountsToSignal(counts)
	}
}

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

// persist: merge-on-write each canonical span event under the row lock, and emit
// the data-derived ingest metrics (per project; never per-span-id labels).
type persistStage struct {
	store   storage.TelemetryStore
	metrics *metrics.Registry
}

func (s *persistStage) Name() string { return "persist" }
func (s *persistStage) Process(ctx context.Context, ing *Ingestion) error {
	proj := ing.Identity.ProjectID
	if s.metrics != nil {
		s.metrics.CounterAdd("llmobs_ingest_bytes_total", "Ingested request bytes.",
			map[string]string{"project_id": proj}, float64(len(ing.Body)))
	}
	for _, ev := range ing.Events {
		if err := s.store.PersistSpan(ctx, ev); err != nil {
			return err
		}
		if s.metrics != nil {
			s.recordSpanMetrics(proj, ev)
		}
	}
	return nil
}

func (s *persistStage) recordSpanMetrics(proj string, ev storage.Event) {
	kind, _ := ev.Payload["kind"].(string)
	if kind == "" {
		kind = "span"
	}
	s.metrics.CounterAdd("llmobs_ingest_spans_total", "Ingested spans by kind.",
		map[string]string{"project_id": proj, "kind": kind}, 1)
	if st, ok := ev.Payload["status"].(map[string]any); ok {
		if code, _ := st["code"].(string); code == "error" {
			s.metrics.CounterAdd("llmobs_error_spans_total", "Ingested error spans.",
				map[string]string{"project_id": proj}, 1)
		}
	}
	if tc, ok := ev.Payload["total_cost"].(float64); ok {
		s.metrics.CounterAdd("llmobs_ingest_cost_total", "Summed total_cost of ingested spans.",
			map[string]string{"project_id": proj}, tc)
	}
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
