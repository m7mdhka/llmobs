package authhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
)

func withSession(r *http.Request, csrf string) *http.Request {
	sess := controlplane.Session{CSRFToken: csrf, User: controlplane.User{ID: "u1", Email: "a@b.c", Role: "admin"}}
	return r.WithContext(context.WithValue(r.Context(), sessionKey, sess))
}

func TestRequireAuthRejectsAnonymous(t *testing.T) {
	h := New(nil, nil, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	h.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous should be 401, got %d", rec.Code)
	}
}

func TestRequireAuthCSRF(t *testing.T) {
	h := New(nil, nil, false)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	// State-changing without CSRF header -> 403.
	rec := httptest.NewRecorder()
	req := withSession(httptest.NewRequest(http.MethodPost, "/x", nil), "secret-csrf")
	h.RequireAuth(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF should be 403, got %d", rec.Code)
	}

	// With correct CSRF header -> passes.
	rec = httptest.NewRecorder()
	req = withSession(httptest.NewRequest(http.MethodPost, "/x", nil), "secret-csrf")
	req.Header.Set(csrfHeader, "secret-csrf")
	h.RequireAuth(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST with matching CSRF should pass, got %d", rec.Code)
	}

	// GET (safe method) needs no CSRF.
	rec = httptest.NewRecorder()
	req = withSession(httptest.NewRequest(http.MethodGet, "/x", nil), "secret-csrf")
	h.RequireAuth(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET with session should pass, got %d", rec.Code)
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	rl := newRateLimiter(3, time.Minute)
	rl.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if !rl.allow("1.2.3.4") {
			t.Fatalf("attempt %d should be allowed", i)
		}
	}
	if rl.allow("1.2.3.4") {
		t.Fatal("4th attempt should be blocked")
	}
	if !rl.allow("5.6.7.8") {
		t.Fatal("a different IP has its own window")
	}
	// After the window elapses, attempts are allowed again.
	now = now.Add(2 * time.Minute)
	if !rl.allow("1.2.3.4") {
		t.Fatal("window should reset after duration")
	}
}
