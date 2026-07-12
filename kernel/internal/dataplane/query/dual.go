package query

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/dualstore"
)

// The dual-read seam. Migrating lite→scale is permanent dual-read: every read
// unifies the historical (lite) and new (scale) backends, so a migrating user
// never sees an empty product. Because compiled SQL is dialect-specific, the
// compiled-SQL query paths cannot fan ONE statement to two engines — so the
// router compiles the SAME DSL doc for BOTH dialects (Postgres for lite,
// ClickHouse for scale), runs each backend, and unifies via the dualstore merge
// helpers. The non-SQL paths (Get*/Erase/PersistScore/GetTraceSpans) go through
// the dualstore.Store decorator. This is the ONE seam every read funnels
// through: the server uses it whenever a dual store is set, so no read resolves
// against a single adapter directly — including the implicit empty-product
// onboarding checks (the list + Get-404 paths).

// listRows is THE convergence seam for compiled-SQL list reads (spans/scores/traces):
// dual-read when a scale store is configured (compile both dialects, run both, merge),
// else the single store. Every list handler funnels through here, so a new one inherits
// dual-read by construction — nothing reads a single adapter directly.
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
	if err != nil {
		return nil, nil, err
	}
	groups, warnings := flagAggTruncation(groups, nil)
	return groups, warnings, nil
}

// flagAggTruncation enforces the no-silent-truncation rule. The adapters over-fetch
// one past MaxAggregationGroups, so a result of cap+1 is PROVABLY truncated: trim it back
// to the cap and emit a LOUD warning, so an incomplete aggregate is never returned as if
// complete (a silent-wrong result the user trusts, and which corrupts the dual-read
// merge). Returns the trimmed groups + any warning appended.
func flagAggTruncation(groups []map[string]any, warnings []string) ([]map[string]any, []string) {
	if len(groups) > storage.MaxAggregationGroups {
		groups = groups[:storage.MaxAggregationGroups]
		warnings = append(warnings, aggTruncationWarning())
	}
	return groups, warnings
}

func aggTruncationWarning() string {
	return fmt.Sprintf(
		"aggregation truncated at %d groups: the GROUP BY cardinality exceeds the cap, so these results are INCOMPLETE. Narrow the query (add filters or a coarser grouping) for a complete answer.",
		storage.MaxAggregationGroups)
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
// scale's partial only — the caller must tell the user it is approximate
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
	// Detect truncation on EITHER store BEFORE the merge. Merging a truncated set
	// with a complete one silently corrupts the combined groups (a group present only in
	// the truncated store's dropped tail looks single-store), so the truncation must be
	// flagged loudly — never a silent wrong merge. Trim each to the cap and warn once.
	var warnings []string
	if len(liteRows) > storage.MaxAggregationGroups || len(scaleRows) > storage.MaxAggregationGroups {
		warnings = append(warnings, aggTruncationWarning())
	}
	liteRows, _ = flagAggTruncation(liteRows, nil)
	scaleRows, _ = flagAggTruncation(scaleRows, nil)
	// Single-store passthrough is exact (no straddle → no approximation).
	if len(liteRows) == 0 {
		return scaleRows, warnings, nil
	}
	if len(scaleRows) == 0 {
		return liteRows, warnings, nil
	}
	spec := dualstore.AggSpec{GroupCols: pg.GroupAliases, Ops: aggOps(pg.AggMetas)}
	merged, nonMergeable := dualstore.MergeAggregation(scaleRows, liteRows, spec)
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
