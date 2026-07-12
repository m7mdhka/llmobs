package pipeline

import (
	"context"
	"log/slog"

	"github.com/m7mdhka/llmobs/kernel/internal/costderive"
	"github.com/m7mdhka/llmobs/kernel/internal/pricing"
)

// PriceResolver is the price-table surface the enrich stage needs (implemented by
// postgres.PriceStore). Declared at the consumer (go-style). Resolve returns the entry
// applying to (provider, model) at a span's time, or (nil, nil) when none exists.
type PriceResolver interface {
	costderive.PriceLookup
	costderive.DiscountLookup
}

// enrich resolves usage_details and derives cost_details/total_cost/cost_source/
// pricing_snapshot_ref for each span, DERIVE-ONCE at ingest (not read time). It is
// fail-soft: a price-lookup error leaves cost null and never fails the pipeline (bad
// data / a transient lookup never breaks a valid ingest; a later re-pricing backfill
// fills it in). No price table configured (nil
// resolver) → a no-op pass-through, so cost is simply absent, never wrong. The actual
// derivation is costderive.DeriveSpanCost — the SAME path the re-pricing backfill uses,
// so an ingest-priced span and a re-priced span compute identically.
type enrichStage struct {
	prices PriceResolver
	log    *slog.Logger
}

func (s *enrichStage) Name() string { return "enrich" }

func (s *enrichStage) Process(ctx context.Context, ing *Ingestion) error {
	if s.prices == nil || len(ing.Events) == 0 {
		return nil
	}
	// Per-batch caches: the discount is one lookup per project (all spans share it), and
	// (provider,model) entries repeat within a batch. Fresh each Process call → never
	// serves a price edit stale beyond one ingest batch.
	discount, _, derr := s.prices.GetDiscount(ctx, ing.Identity.ProjectID)
	if derr != nil {
		// A discount-lookup error must NOT derive at full price — that would silently
		// OVERCHARGE a discounted project and stamp cost_source=derived, which a
		// null-based re-pricing backfill cannot detect (the failure-path trap: the
		// failure path producing wrong-but-plausible data). Instead skip derivation for
		// this batch, leaving cost NULL — the backfill re-prices it correctly later. This
		// mirrors the price-resolve failure (also null, backfillable).
		if s.log != nil {
			s.log.Warn("enrich: discount lookup failed; leaving cost null (backfill will re-price)", "err", derr.Error())
		}
		return nil
	}
	entryCache := map[string]*pricing.Entry{}
	for _, ev := range ing.Events {
		if _, err := costderive.DeriveSpanCost(ctx, ev.Payload, s.prices, discount, entryCache); err != nil {
			// Fail-soft: a price-resolve blip leaves this span's cost null (never a
			// wrong or fabricated value); the re-pricing backfill fills it later. At
			// INGEST nulling is safe — the cost was never set. (The backfill, by contrast,
			// STOPS on this error, because there nulling would drop an existing cost.)
			if s.log != nil {
				s.log.Warn("enrich: price resolve failed; leaving cost null", "err", err.Error())
			}
		}
	}
	return nil
}
