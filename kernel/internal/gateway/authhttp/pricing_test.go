package authhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/pricing"
)

// fakePriceOps records whether a write reached the store.
type fakePriceOps struct {
	upserted   bool
	discounted bool
}

func (f *fakePriceOps) ListCurrent(context.Context) ([]pricing.Entry, error) { return nil, nil }
func (f *fakePriceOps) ListVersions(context.Context, string, string) ([]pricing.Entry, error) {
	return nil, nil
}
func (f *fakePriceOps) Upsert(_ context.Context, in pricing.Entry, _ string) (*pricing.Entry, error) {
	f.upserted = true
	e := in
	e.Version = 1
	e.ID = pricing.EntryID(pricing.CanonicalProvider(in.Provider), pricing.CanonicalModel(in.Model), 1)
	return &e, nil
}
func (f *fakePriceOps) GetDiscount(context.Context, string) (float64, bool, error) {
	return 0, false, nil
}
func (f *fakePriceOps) SetDiscount(context.Context, string, float64, string) error {
	f.discounted = true
	return nil
}

func withRole(r *http.Request, role, csrf string) *http.Request {
	sess := controlplane.Session{CSRFToken: csrf, User: controlplane.User{ID: "u1", Email: "a@b.c", Role: role}}
	return r.WithContext(context.WithValue(r.Context(), sessionKey, sess))
}

// TestPricingWriteAuthz is the price-edit write-path proof (a NEW write surface): an
// entry edit requires an authenticated session (RequireAuth 401), a valid CSRF token
// (RequireAuth 403), AND admin/config authority (403 for a viewer). Reads are open to
// any session. This is the authz matrix the adversarial review targets.
func TestPricingWriteAuthz(t *testing.T) {
	h := New(nil, nil, false)
	store := &fakePriceOps{}
	mux := http.NewServeMux()
	h.RegisterPricing(mux, store)

	body := `{"provider":"openai","model":"gpt-4o","effective_from":"2026-01-01T00:00:00Z","rates":{"input":{"per_token":0.0000025}}}`
	post := func(mod func(*http.Request) *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1alpha1/pricing", strings.NewReader(body))
		mux.ServeHTTP(rec, mod(req))
		return rec
	}

	// Anonymous (no session) → 401 (RequireAuth).
	if rec := post(func(r *http.Request) *http.Request { return r }); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous price edit must be 401, got %d", rec.Code)
	}
	// Admin session but NO CSRF → 403 (RequireAuth).
	if rec := post(func(r *http.Request) *http.Request { return withRole(r, "admin", "tok") }); rec.Code != http.StatusForbidden {
		t.Fatalf("price edit without CSRF must be 403, got %d", rec.Code)
	}
	// Viewer session WITH CSRF → 403 (canWrite: no config authority).
	if rec := post(func(r *http.Request) *http.Request {
		r = withRole(r, "viewer", "tok")
		r.Header.Set(csrfHeader, "tok")
		return r
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer price edit must be 403, got %d", rec.Code)
	}
	if store.upserted {
		t.Fatal("no unauthorized request may reach the store")
	}
	// Admin + CSRF → 201, store reached.
	rec := post(func(r *http.Request) *http.Request {
		r = withRole(r, "admin", "tok")
		r.Header.Set(csrfHeader, "tok")
		return r
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin price edit with CSRF must be 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !store.upserted {
		t.Fatal("authorized edit must reach the store")
	}

	// Reads are open to any authenticated session (viewer, GET, no CSRF needed).
	rec = httptest.NewRecorder()
	req := withRole(httptest.NewRequest(http.MethodGet, "/v1alpha1/pricing", nil), "viewer", "")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer read must be 200, got %d", rec.Code)
	}
}
