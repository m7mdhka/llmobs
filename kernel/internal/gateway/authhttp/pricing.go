package authhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/pricing"
)

// PriceOps is the price-table surface the control-plane API needs (implemented by
// postgres.PriceStore). Declared here so authhttp does not depend on the storage
// package. Global price entries are instance-wide; discounts are per-project.
type PriceOps interface {
	ListCurrent(ctx context.Context) ([]pricing.Entry, error)
	ListVersions(ctx context.Context, provider, model string) ([]pricing.Entry, error)
	Upsert(ctx context.Context, in pricing.Entry, actor string) (*pricing.Entry, error)
	GetDiscount(ctx context.Context, projectID string) (float64, bool, error)
	SetDiscount(ctx context.Context, projectID string, factor float64, actor string) error
}

// RepriceStarter starts a background re-pricing run (Arc M / M4). Implemented by
// *reprice.Launcher; declared here so authhttp stays free of the storage/reprice
// packages. It returns the run key and whether a new run started (false = an identical run
// was already in flight). A nil starter disables the trigger endpoint.
type RepriceStarter interface {
	StartReprice(snapshotRefID, projectID string) (runKey string, started bool)
}

// RegisterPricing mounts the price-table management endpoints (ADR-0029). All are
// session-authenticated and CSRF-protected on mutating methods (via RequireAuth).
// READS are open to any authenticated session; WRITES additionally require
// configuration authority (admin today, the #21 RBAC seam) — a viewer cannot change
// what every tenant on the instance is billed. Global price entries carry NO project;
// only the per-project discount is tenant-scoped, to the caller's own default project.
// reprice (M4) MAY be nil (re-pricing disabled); when set it mounts the admin-gated
// re-price trigger.
func (h *Handler) RegisterPricing(mux *http.ServeMux, store PriceOps, reprice RepriceStarter) {
	p := &pricingHandler{h: h, store: store, reprice: reprice}
	mux.Handle("/v1alpha1/pricing", h.RequireAuth(http.HandlerFunc(p.collection)))
	mux.Handle("/v1alpha1/pricing/", h.RequireAuth(http.HandlerFunc(p.sub)))
}

type pricingHandler struct {
	h       *Handler
	store   PriceOps
	reprice RepriceStarter
}

// canWrite reports whether the session may change pricing (admin/write authority).
//
// O2 note: this gates on the session's DEFAULT-ORG role (sess.User.Role). Pricing entries
// are a GLOBAL, instance-wide table (no project/org), so "may edit prices" is genuinely a
// global-admin question, not a per-project one — the default-org role is a defensible
// residual in the single-org profiles. When multi-org lands (O3+), who may edit global
// prices (a user who is admin in one org, viewer in another) is a ruled decision for that
// arc; the per-project discount write should then resolve against the discount's project
// org (perm via RoleForProject), like settings does.
func (p *pricingHandler) canWrite(r *http.Request) (string, bool) {
	sess, ok := SessionFrom(r.Context())
	if !ok || !perm.HasWriteAuthority(perm.RoleScopes(sess.User.Role)) {
		return "", false
	}
	return "session:" + sess.User.Email, true
}

// collection: GET /v1alpha1/pricing (list current entries) | POST (upsert a new version).
func (p *pricingHandler) collection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		entries, err := p.store.ListCurrent(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "list failed")
			return
		}
		if entries == nil {
			entries = []pricing.Entry{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
	case http.MethodPost:
		actor, ok := p.canWrite(r)
		if !ok {
			writeErr(w, http.StatusForbidden, "forbidden", "editing global prices requires an admin session")
			return
		}
		var in pricing.Entry
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields() // reject fields outside the price-entry schema
		if err := dec.Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, "schema_invalid", "invalid JSON or unknown field")
			return
		}
		saved, err := p.store.Upsert(r.Context(), in, actor)
		if errors.Is(err, pricing.ErrVersionConflict) {
			writeErr(w, http.StatusConflict, "conflict", "a concurrent edit won; retry")
			return
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, "schema_invalid", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, saved)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST")
	}
}

// sub routes /v1alpha1/pricing/{versions|discount}.
func (p *pricingHandler) sub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1alpha1/pricing/")
	switch {
	case rest == "versions":
		p.versions(w, r)
	case rest == "discount":
		p.discount(w, r)
	case rest == "reprice":
		p.repriceTrigger(w, r)
	default:
		writeErr(w, http.StatusNotFound, "not_found", "unknown pricing resource")
	}
}

// repriceTrigger: POST /v1alpha1/pricing/reprice — start a background re-pricing run
// (M4, 06-usage-cost.md §5). Admin-gated exactly like a price/discount edit, because it
// MUTATES money across history. Two scopes:
//   - scope="price" (default): re-price spans priced against a superseded version of
//     (provider, model, version). GLOBAL across projects — a price applies instance-wide.
//   - scope="discount": re-price the caller's OWN project's derived spans after a discount
//     change. Tenant-scoped: projectID is server-derived, never caller-supplied, so a
//     caller can never re-price another tenant's spans.
func (p *pricingHandler) repriceTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	if p.reprice == nil {
		writeErr(w, http.StatusNotImplemented, "not_implemented", "re-pricing is not enabled on this instance")
		return
	}
	if _, ok := p.canWrite(r); !ok {
		writeErr(w, http.StatusForbidden, "forbidden", "re-pricing requires an admin session")
		return
	}
	var req struct {
		Scope    string `json:"scope"` // "price" (default) | "discount"
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Version  int    `json:"version"` // the SUPERSEDED price version to re-price off
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "schema_invalid", "invalid JSON or unknown field")
		return
	}

	var snapshotRefID, projectID string
	switch req.Scope {
	case "", "price":
		if req.Provider == "" || req.Model == "" || req.Version <= 0 {
			writeErr(w, http.StatusBadRequest, "schema_invalid", "provider, model, and a positive version are required")
			return
		}
		// Canonicalize so the ref id matches how derivation stamped it (R6) — a raw
		// spelling ("Google") must resolve to the same id as the canonical one.
		snapshotRefID = pricing.EntryID(pricing.CanonicalProvider(req.Provider), pricing.CanonicalModel(req.Model), req.Version)
	case "discount":
		// Server-derived project (never caller-supplied) — the tenant-isolation seam,
		// same as the discount get/set handler.
		pid, err := controlplane.DefaultProjectID(r.Context(), p.h.pool)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "no project")
			return
		}
		projectID = pid
	default:
		writeErr(w, http.StatusBadRequest, "schema_invalid", "scope must be 'price' or 'discount'")
		return
	}

	runKey, started := p.reprice.StartReprice(snapshotRefID, projectID)
	writeJSON(w, http.StatusAccepted, map[string]any{"run_key": runKey, "started": started})
}

// versions: GET /v1alpha1/pricing/versions?provider=&model= — the price history for one
// (provider, model), newest first (re-derivability / audit).
func (p *pricingHandler) versions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	provider := r.URL.Query().Get("provider")
	model := r.URL.Query().Get("model")
	if provider == "" || model == "" {
		writeErr(w, http.StatusBadRequest, "schema_invalid", "provider and model are required")
		return
	}
	entries, err := p.store.ListVersions(r.Context(), provider, model)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	if entries == nil {
		entries = []pricing.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// discount: GET (this project's discount) | PUT (set it). The discount is scoped to the
// caller's OWN default project — a caller can never read or set another tenant's.
func (p *pricingHandler) discount(w http.ResponseWriter, r *http.Request) {
	// SINGLE-PROJECT ASSUMPTION: DefaultProjectID returns the instance's one project
	// (lite). It is server-derived (never caller-supplied), so today a caller cannot
	// target another tenant. When multi-project lands, this MUST become a
	// session→project resolution (the session's project, not a global LIMIT 1), or
	// every caller's discount get/set would hit the same project — a cross-tenant seam.
	projectID, err := controlplane.DefaultProjectID(r.Context(), p.h.pool)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "no project")
		return
	}
	switch r.Method {
	case http.MethodGet:
		factor, ok, err := p.store.GetDiscount(r.Context(), projectID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "read failed")
			return
		}
		resp := map[string]any{"project_id": projectID, "factor": nil}
		if ok {
			resp["factor"] = factor
		}
		writeJSON(w, http.StatusOK, resp)
	case http.MethodPut:
		actor, ok := p.canWrite(r)
		if !ok {
			writeErr(w, http.StatusForbidden, "forbidden", "setting a discount requires an admin session")
			return
		}
		var req struct {
			Factor float64 `json:"factor"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "schema_invalid", "invalid JSON")
			return
		}
		if err := p.store.SetDiscount(r.Context(), projectID, req.Factor, actor); err != nil {
			writeErr(w, http.StatusBadRequest, "schema_invalid", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project_id": projectID, "factor": req.Factor})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or PUT")
	}
}
