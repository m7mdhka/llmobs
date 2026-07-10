package platform

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/ingesthealth"
)

// TestReadyzPersistHealthGate proves the G2 readiness gate: when the persist
// signal is unhealthy, /readyz returns 503 BEFORE touching the pool (a
// connectable-but-unwritable DB must still read not-ready). The nil pool here is
// deliberate — the short-circuit means it is never dereferenced.
func TestReadyzPersistHealthGate(t *testing.T) {
	sig := ingesthealth.New(1)
	sig.RecordPersist(errors.New("disk full")) // flip unhealthy

	mux := http.NewServeMux()
	NewHealth(nil, sig).Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy persistence must make /readyz not-ready, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "persistence") {
		t.Fatalf("readiness reason should name persistence, got %q", rec.Body.String())
	}

	// Liveness is independent of persist health — the process is still alive.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz should stay 200 regardless of persist health, got %d", rec.Code)
	}
}
