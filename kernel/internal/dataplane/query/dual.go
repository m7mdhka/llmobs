package query

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/m7mdhka/llmobs/kernel/internal/storage/dualstore"
)

// The dual-read seam (ADR-0026 RULING-MIG6). Because compiled SQL is
// dialect-specific, the compiled-SQL query paths cannot fan ONE statement to two
// engines — so the router compiles the SAME DSL doc for BOTH dialects (Postgres for
// lite, ClickHouse for scale), runs each backend, and unifies via the dualstore
// merge helpers. The non-SQL paths (Get*/Erase/PersistScore/GetTraceSpans) go
// through the dualstore.Store decorator. This is the ONE seam every read funnels
// through (R-MIG4): the server uses it whenever a dual store is set, so no read
// resolves against a single adapter directly — including the implicit
// empty-product onboarding checks (the list + Get-404 paths).

// listRows is THE convergence seam for compiled-SQL list reads (spans/scores/traces):
// dual-read when a scale store is configured (compile both dialects, run both, merge),
// else the single store. Every list handler funnels through here, so a new one inherits
// dual-read by construction (invariant #11) — nothing reads a single adapter directly.
func (s *Server) listRows(ctx context.Context, target string, doc map[string]any, projectID string, c *Compiled) ([]json.RawMessage, error) {
	if s.dual != nil {
		return s.dualRows(ctx, target, doc, projectID, c.Limit+1)
	}
	switch target {
	case "traces":
		return s.store.QueryTraces(ctx, c.Where, c.Args, c.Order, c.Limit+1)
	case "scores":
		return s.store.QueryScores(ctx, c.Where, c.Args, c.Order, c.Limit+1)
	default:
		return s.store.QuerySpans(ctx, c.Where, c.Args, c.Order, c.Limit+1)
	}
}

// aggRows is the convergence seam for aggregation reads: dual-read (compile both
// dialects, merge, surface straddle warnings) when configured, else the single store.
func (s *Server) aggRows(ctx context.Context, target string, doc map[string]any, projectID string, ca *CompiledAgg) ([]map[string]any, []string, error) {
	if s.dual != nil {
		return s.dualAgg(ctx, target, doc, projectID)
	}
	groups, err := s.store.QueryAggregation(ctx, target, ca.Select, ca.Where, ca.GroupBy, ca.Args)
	return groups, nil, err
}

// dualRows runs a spans/scores/traces list query across both backends and merges.
// limit is the server's over-fetch (c.Limit+1); order is the dialect-neutral ORDER.
func (s *Server) dualRows(ctx context.Context, target string, doc map[string]any, projectID string, limit int) ([]json.RawMessage, error) {
	lite, scale := s.dual.Backends()

	compile := func(d Dialect) (*Compiled, error) {
		switch target {
		case "spans":
			return CompileSpansForDialect(doc, projectID, s.maxWindow, d)
		case "traces":
			return CompileTracesForDialect(doc, projectID, s.maxWindow, d)
		case "scores":
			return CompileScoresForDialect(doc, projectID, s.maxWindow, d)
		}
		return nil, fmt.Errorf("unknown target %q", target)
	}
	pg, err := compile(PostgresDialect)
	if err != nil {
		return nil, err
	}
	chc, err := compile(ClickHouseDialect)
	if err != nil {
		return nil, err
	}

	keys := orderKeys(pg.OrderKeys)
	switch target {
	case "traces":
		liteT, err := lite.QueryTraces(ctx, pg.Where, pg.Args, pg.Order, limit)
		if err != nil {
			return nil, err
		}
		scaleT, err := scale.QueryTraces(ctx, chc.Where, chc.Args, chc.Order, limit)
		if err != nil {
			return nil, err
		}
		return dualstore.MergeTraces(ctx, scaleT, liteT, keys, limit, s.dual.GetTraceSpans)
	case "scores":
		liteR, err := lite.QueryScores(ctx, pg.Where, pg.Args, pg.Order, limit)
		if err != nil {
			return nil, err
		}
		scaleR, err := scale.QueryScores(ctx, chc.Where, chc.Args, chc.Order, limit)
		if err != nil {
			return nil, err
		}
		return trim(dualstore.MergeOrdered(scaleR, liteR, keys), limit), nil
	default:
		liteR, err := lite.QuerySpans(ctx, pg.Where, pg.Args, pg.Order, limit)
		if err != nil {
			return nil, err
		}
		scaleR, err := scale.QuerySpans(ctx, chc.Where, chc.Args, chc.Order, limit)
		if err != nil {
			return nil, err
		}
		return trim(dualstore.MergeOrdered(scaleR, liteR, keys), limit), nil
	}
}

// orderKeys adapts the query package's logical OrderKeys to the dualstore merge's
// dialect-neutral keys (the merge can't import the query package).
func orderKeys(ks []OrderKey) []dualstore.OrderKey {
	out := make([]dualstore.OrderKey, len(ks))
	for i, k := range ks {
		out[i] = dualstore.OrderKey{Field: k.Field, Desc: k.Desc}
	}
	return out
}

// dualAgg runs an aggregation across both backends and merges group rows. It also
// returns human-facing warnings: when a non-mergeable aggregate (avg, count_distinct,
// percentile) is computed over a group that straddles BOTH stores, the merged value is
// scale's partial only (ADR-0026 D8) — the caller must tell the user it is approximate
// until that group's data is single-store (e.g. after backfill).
func (s *Server) dualAgg(ctx context.Context, target string, doc map[string]any, projectID string) ([]map[string]any, []string, error) {
	lite, scale := s.dual.Backends()
	pg, err := CompileAggregationForDialect(doc, projectID, s.maxWindow, target, PostgresDialect)
	if err != nil {
		return nil, nil, err
	}
	chc, err := CompileAggregationForDialect(doc, projectID, s.maxWindow, target, ClickHouseDialect)
	if err != nil {
		return nil, nil, err
	}
	liteRows, err := lite.QueryAggregation(ctx, target, pg.Select, pg.Where, pg.GroupBy, pg.Args)
	if err != nil {
		return nil, nil, err
	}
	scaleRows, err := scale.QueryAggregation(ctx, target, chc.Select, chc.Where, chc.GroupBy, chc.Args)
	if err != nil {
		return nil, nil, err
	}
	// Single-store passthrough is exact (no straddle → no approximation).
	if len(liteRows) == 0 {
		return scaleRows, nil, nil
	}
	if len(scaleRows) == 0 {
		return liteRows, nil, nil
	}
	spec := dualstore.AggSpec{GroupCols: pg.GroupAliases, Ops: aggOps(pg.AggMetas)}
	merged, nonMergeable := dualstore.MergeAggregation(scaleRows, liteRows, spec)
	var warnings []string
	if len(nonMergeable) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"columns %v are approximate: a non-mergeable aggregate (avg/count_distinct/percentile) "+
				"was computed over data straddling both the lite and scale stores; the value reflects "+
				"the scale store only. Run the lite→scale backfill for exact results.", nonMergeable))
	}
	return merged, warnings, nil
}

// aggOps maps each aggregate result column's alias to its canonical op, so the merge
// classifies by op (not a spoofable name prefix).
func aggOps(metas []AggMeta) map[string]string {
	m := make(map[string]string, len(metas))
	for _, a := range metas {
		m[a.Alias] = a.Op
	}
	return m
}

func trim(rows []json.RawMessage, limit int) []json.RawMessage {
	if len(rows) > limit {
		return rows[:limit]
	}
	return rows
}
