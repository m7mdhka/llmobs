package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/pricing"
)

// PriceStore is the append-only, versioned price table (ADR-0029). An edit to a
// (provider, model) price INSERTs a new version; prior versions are never mutated, so a
// derived cost's pricing_snapshot_ref stays resolvable and re-pricing is deterministic.
// Provider/model are always stored CANONICAL (internal/pricing.Canonical*), applied here
// at the write seam so a lookup with the same normalizer always hits (R6). Prices are
// instance-global; the only tenant-scoped surface is the per-project discount.
type PriceStore struct {
	pool *pgxpool.Pool
}

func NewPriceStore(pool *pgxpool.Pool) *PriceStore { return &PriceStore{pool: pool} }

// Resolve returns the price entry that applies to a span of (rawProvider, rawModel) at
// time `at`: the newest version whose effective_from <= at, after canonicalizing the
// inputs with the SAME normalizer used at write. Returns (nil, nil) when no entry
// matches — the caller leaves cost null (§4 step 3), never fabricates a zero.
func (s *PriceStore) Resolve(ctx context.Context, rawProvider, rawModel string, at time.Time) (*pricing.Entry, error) {
	prov := pricing.CanonicalProvider(rawProvider)
	model := pricing.CanonicalModel(rawModel)
	// 1) The exact model (+ dot/dash fallback, #106).
	if e, err := s.resolveModel(ctx, prov, model, at); e != nil || err != nil {
		return e, err
	}
	// 2) DATE-SUFFIX fallback (#102) — ONLY when the model (with its date) missed. Providers
	//    publish dated snapshots ("gpt-4o-2024-05-13", "claude-3-5-sonnet-20241022",
	//    "gpt-4-0613") whose price table often carries just the base model; the dated variant
	//    would otherwise silently derive NO cost. Strip a trailing date suffix and re-resolve the
	//    base (which itself gets the dot/dash fallback). Conservative: only an ISO date, a
	//    compact YYYYMMDD, or a VALID MMDD snapshot is treated as a date — a trailing 4-digit run
	//    that isn't a real month/day (e.g. "-1234") is left intact, so a genuine model segment is
	//    never mistaken for a date. Stored keys are untouched (R6 primary path unchanged).
	if base, ok := stripDateSuffix(model); ok {
		return s.resolveModel(ctx, prov, base, at)
	}
	return nil, nil
}

// resolveModel resolves a canonical model with the EXACT match first (R6 primary-key path),
// then the dot/dash-insensitive fallback (#106). Shared by Resolve's exact and date-stripped
// attempts.
func (s *PriceStore) resolveModel(ctx context.Context, prov, model string, at time.Time) (*pricing.Entry, error) {
	e, err := s.resolveWhere(ctx,
		`provider=$1 AND model=$2 AND effective_from <= $3`, prov, model, at.UTC())
	if e != nil || err != nil {
		return e, err
	}
	if strings.ContainsAny(model, ".-") {
		return s.resolveWhere(ctx,
			`provider=$1 AND translate(model, '.', '-') = translate($2, '.', '-') AND effective_from <= $3`,
			prov, model, at.UTC())
	}
	return nil, nil
}

var (
	dateSuffixRe = regexp.MustCompile(`-(\d{4}-\d{2}-\d{2}|\d{8})$`) // -YYYY-MM-DD or -YYYYMMDD
	mmddSuffixRe = regexp.MustCompile(`-(\d{4})$`)                   // -MMDD (OpenAI snapshot, validated)
)

// stripDateSuffix removes a trailing provider date/snapshot suffix from a canonical model,
// returning the base and whether a suffix was stripped. It is deliberately conservative to
// avoid false matches: an ISO date (-YYYY-MM-DD) or compact date (-YYYYMMDD) is always a date;
// a 4-digit -MMDD is treated as a date ONLY when MM∈[1,12] and DD∈[1,31] (so "-1234" or a
// legitimate numeric model segment is not mistaken for a snapshot).
func stripDateSuffix(model string) (string, bool) {
	if loc := dateSuffixRe.FindStringIndex(model); loc != nil {
		return model[:loc[0]], true
	}
	if m := mmddSuffixRe.FindStringSubmatch(model); m != nil {
		mm, _ := strconv.Atoi(m[1][:2])
		dd, _ := strconv.Atoi(m[1][2:])
		if mm >= 1 && mm <= 12 && dd >= 1 && dd <= 31 {
			return model[:len(model)-len(m[0])], true
		}
	}
	return model, false
}

// resolveWhere runs the price-entry lookup with the given WHERE predicate (parameters
// $1=provider, $2=model, $3=at), newest-effective + highest-version first. ErrNoRows → nil.
func (s *PriceStore) resolveWhere(ctx context.Context, where string, prov, model string, at time.Time) (*pricing.Entry, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, provider, model, version, effective_from, rates, tiers, source, raw_provider, created_by, created_at
		   FROM price_entries
		  WHERE `+where+`
		  ORDER BY effective_from DESC, version DESC
		  LIMIT 1`, prov, model, at)
	e, err := scanEntry(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// GetByID returns the exact entry a pricing_snapshot_ref points at (for re-pricing /
// display), or (nil, nil) if it no longer exists.
func (s *PriceStore) GetByID(ctx context.Context, id string) (*pricing.Entry, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, provider, model, version, effective_from, rates, tiers, source, raw_provider, created_by, created_at
		   FROM price_entries WHERE id=$1`, id)
	e, err := scanEntry(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// Upsert appends the NEXT version for a (provider, model) and returns the stored entry.
// It canonicalizes provider/model, computes the next monotonic version under an
// advisory lock (so concurrent edits serialize), stamps the id, and inserts. The raw
// provider string is preserved verbatim. `source` defaults to "override" for an
// operator edit; pass "default" only for seeding.
func (s *PriceStore) Upsert(ctx context.Context, in pricing.Entry, actor string) (*pricing.Entry, error) {
	prov := pricing.CanonicalProvider(in.Provider)
	model := pricing.CanonicalModel(in.Model)
	if prov == "" || model == "" {
		return nil, errors.New("price entry requires provider and model")
	}
	if len(in.Rates) == 0 {
		return nil, errors.New("price entry requires at least one rate")
	}
	if err := in.ValidateRates(); err != nil {
		return nil, err
	}
	eff := time.Now().UTC()
	if in.EffectiveFrom != "" {
		t, err := time.Parse(time.RFC3339, in.EffectiveFrom)
		if err != nil {
			return nil, fmt.Errorf("invalid effective_from: %w", err)
		}
		eff = t.UTC()
	}
	// An API write is always an operator override; the caller-supplied `source` is
	// ignored so a "default" provenance cannot be spoofed (only SeedDefaults writes
	// 'default', and it does not go through Upsert). created_by is server-derived below.
	source := "override"
	ratesJSON, err := json.Marshal(in.Rates)
	if err != nil {
		return nil, err
	}
	tiers := in.Tiers
	if tiers == nil {
		tiers = []pricing.Tier{}
	}
	tiersJSON, err := json.Marshal(tiers)
	if err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialize concurrent edits to the same (provider, model) so version numbers don't
	// collide (the UNIQUE constraint is the backstop; the lock avoids the race).
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, prov+"\x1e"+model); err != nil {
		return nil, err
	}
	var next int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version),0)+1 FROM price_entries WHERE provider=$1 AND model=$2`,
		prov, model).Scan(&next); err != nil {
		return nil, err
	}
	id := pricing.EntryID(prov, model, next)
	var rawProv any
	if in.RawProvider != "" {
		rawProv = in.RawProvider
	}
	var createdBy any
	if actor != "" {
		createdBy = actor
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO price_entries (id, provider, model, version, effective_from, rates, tiers, source, raw_provider, created_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		id, prov, model, next, eff, ratesJSON, tiersJSON, source, rawProv, createdBy); err != nil {
		if isUniqueViolation(err) {
			return nil, pricing.ErrVersionConflict
		}
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetByID(ctx, id)
}

// ListCurrent returns the CURRENTLY-EFFECTIVE version of each (provider, model) — the
// same version derivation would bill right now (newest with effective_from <= now),
// NOT merely the highest version number. A future-dated correction is not yet
// "current", and a back-dated one is resolved by effective_from — so the operator's
// "current" view matches what is actually charged (Resolve uses the identical rule).
func (s *PriceStore) ListCurrent(ctx context.Context) ([]pricing.Entry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT ON (provider, model)
		        id, provider, model, version, effective_from, rates, tiers, source, raw_provider, created_by, created_at
		   FROM price_entries
		  WHERE effective_from <= now()
		  ORDER BY provider, model, effective_from DESC, version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectEntries(rows)
}

// ListVersions returns every version of one (provider, model), newest first — the price
// history for re-derivability / audit.
func (s *PriceStore) ListVersions(ctx context.Context, rawProvider, rawModel string) ([]pricing.Entry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, provider, model, version, effective_from, rates, tiers, source, raw_provider, created_by, created_at
		   FROM price_entries WHERE provider=$1 AND model=$2 ORDER BY version DESC`,
		pricing.CanonicalProvider(rawProvider), pricing.CanonicalModel(rawModel))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectEntries(rows)
}

// GetDiscount returns a project's discount factor, or (0, false) if unset.
func (s *PriceStore) GetDiscount(ctx context.Context, projectID string) (float64, bool, error) {
	var f float64
	err := s.pool.QueryRow(ctx, `SELECT factor FROM price_discounts WHERE project_id=$1`, projectID).Scan(&f)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return f, true, nil
}

// SetDiscount upserts a project's discount multiplier. factor must be in (0, 1] — a
// discount, never a markup (a factor > 1 would inflate cost, which is not a discount).
func (s *PriceStore) SetDiscount(ctx context.Context, projectID string, factor float64, actor string) error {
	// NaN slips a bare (0,1] comparison (both sides false), so reject non-finite first —
	// the HTTP decoder already rejects NaN tokens, but a non-HTTP caller must not either.
	if math.IsNaN(factor) || math.IsInf(factor, 0) || factor <= 0 || factor > 1 {
		return errors.New("discount factor must be a finite number in (0, 1]")
	}
	var by any
	if actor != "" {
		by = actor
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO price_discounts (project_id, factor, updated_by, updated_at)
		 VALUES ($1,$2,$3, now())
		 ON CONFLICT (project_id) DO UPDATE SET factor=EXCLUDED.factor, updated_by=EXCLUDED.updated_by, updated_at=now()`,
		projectID, factor, by)
	return err
}

// SeedDefaults inserts version-1 default price entries that do not already exist
// (idempotent, ON CONFLICT DO NOTHING on the natural key). Seeds establish a working
// baseline; an operator edits/adds via the API. Called at boot after migrations.
func (s *PriceStore) SeedDefaults(ctx context.Context) error {
	for _, e := range defaultPriceSeeds() {
		// Defense in depth: a future bad edit to the static seed set is rejected by the
		// same money-integrity guard the API write path uses (no negative/NaN/Inf rates).
		if err := e.ValidateRates(); err != nil {
			return fmt.Errorf("invalid default price seed %s/%s: %w", e.Provider, e.Model, err)
		}
		prov := pricing.CanonicalProvider(e.Provider)
		model := pricing.CanonicalModel(e.Model)
		id := pricing.EntryID(prov, model, 1)
		ratesJSON, err := json.Marshal(e.Rates)
		if err != nil {
			return err
		}
		tiers := e.Tiers
		if tiers == nil {
			tiers = []pricing.Tier{}
		}
		tiersJSON, err := json.Marshal(tiers)
		if err != nil {
			return err
		}
		eff := time.Unix(0, 0).UTC() // apply to all historical spans
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO price_entries (id, provider, model, version, effective_from, rates, tiers, source)
			 VALUES ($1,$2,$3,1,$4,$5,$6,'default')
			 ON CONFLICT (provider, model, version) DO NOTHING`,
			id, prov, model, eff, ratesJSON, tiersJSON); err != nil {
			return err
		}
	}
	return nil
}

// ---- scanning helpers ----

type scanner interface {
	Scan(dest ...any) error
}

func scanEntry(row scanner) (*pricing.Entry, error) {
	var (
		e         pricing.Entry
		eff, crt  time.Time
		ratesRaw  []byte
		tiersRaw  []byte
		rawProv   *string
		createdBy *string
	)
	if err := row.Scan(&e.ID, &e.Provider, &e.Model, &e.Version, &eff, &ratesRaw, &tiersRaw,
		&e.Source, &rawProv, &createdBy, &crt); err != nil {
		return nil, err
	}
	e.EffectiveFrom = eff.UTC().Format(time.RFC3339)
	e.CreatedAt = crt.UTC().Format(time.RFC3339)
	if rawProv != nil {
		e.RawProvider = *rawProv
	}
	if createdBy != nil {
		e.CreatedBy = *createdBy
	}
	if err := json.Unmarshal(ratesRaw, &e.Rates); err != nil {
		return nil, fmt.Errorf("decode rates for %s: %w", e.ID, err)
	}
	if len(tiersRaw) > 0 {
		if err := json.Unmarshal(tiersRaw, &e.Tiers); err != nil {
			return nil, fmt.Errorf("decode tiers for %s: %w", e.ID, err)
		}
	}
	return &e, nil
}

func collectEntries(rows pgx.Rows) ([]pricing.Entry, error) {
	var out []pricing.Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}
