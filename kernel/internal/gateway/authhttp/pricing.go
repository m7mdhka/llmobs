package authhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
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

// RepriceStarter starts a background re-pricing run. Implemented by
// *reprice.Launcher; declared here so authhttp stays free of the storage/reprice
// packages. It returns the run key and whether a new run started (false = an identical run
// was already in flight). A nil starter disables the trigger endpoint.
type RepriceStarter interface {
	StartReprice(snapshotRefID, projectID string) (runKey string, started bool)
}

// RegisterPricing mounts the price-table management endpoints. All are
// session-authenticated and CSRF-protected on mutating methods (via RequireAuth).
// READS are open to any authenticated session. WRITES split by scope: editing
// the GLOBAL price table (one table every tenant is billed against) requires instance-admin
// (an owner of the default org); the per-project DISCOUNT requires configuration authority in
// that project's own org (any role with write authority, resolved per-project — not "admin"
// specifically, and never an ambient default-org role). reprice MAY be nil (re-pricing
// disabled); when set it mounts the same split gate on its two scopes.
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

// Pricing authority splits the one question the old single canWrite conflated into the two
// it actually is:
//
//   - GLOBAL price entries are ONE instance-wide table shared by every tenant. Editing them
//     (upsert, or re-pricing history off a superseded version) is genuinely an INSTANCE-level
//     action — gated on h.instanceAdmin (an owner of the default org), NOT an ambient per-org
//     role. The authority is named explicitly rather than reusing a per-org role globally.
//   - The per-project DISCOUNT is tenant config — gated on h.writeAuthorityInProject, resolved
//     against the discount's own project org (like plugin settings), never a default-org role.

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
		actor, ok := p.h.instanceAdmin(r)
		if !ok {
			writeErr(w, http.StatusForbidden, "forbidden", "editing global prices requires instance-admin (an owner of the default org)")
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

// repriceTrigger: POST /v1alpha1/pricing/reprice — start a background re-pricing run.
// Admin-gated exactly like a price/discount edit, because it
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
		// Re-pricing off a GLOBAL price version mutates cost across EVERY tenant's history —
		// an instance-level action, gated on instance-admin (not an ambient per-org role).
		if _, ok := p.h.instanceAdmin(r); !ok {
			writeErr(w, http.StatusForbidden, "forbidden", "re-pricing global prices requires instance-admin (an owner of the default org)")
			return
		}
		if req.Provider == "" || req.Model == "" || req.Version <= 0 {
			writeErr(w, http.StatusBadRequest, "schema_invalid", "provider, model, and a positive version are required")
			return
		}
		// Canonicalize so the ref id matches how derivation stamped it — a raw
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
		// Re-pricing THIS project's spans after a discount change is per-project config —
		// gated on write authority in that project's org.
		if _, ok := p.h.writeAuthorityInProject(r, pid); !ok {
			writeErr(w, http.StatusForbidden, "forbidden", "re-pricing this project requires configuration authority in its org")
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
		actor, ok := p.h.writeAuthorityInProject(r, projectID)
		if !ok {
			writeErr(w, http.StatusForbidden, "forbidden", "setting a discount requires configuration authority in this project's org")
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
