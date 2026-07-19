package pluginapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/blob"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginauth"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

func blobsSetup(t *testing.T) (*Blobs, *plugintoken.Signer) {
	t.Helper()
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	store, err := blob.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewBlobs(pluginauth.New(signer, nil), store, 1<<20), signer
}

func blobTokens(t *testing.T, signer *plugintoken.Signer, pluginID, projectID string, caps ...string) (svc, asr string) {
	t.Helper()
	now := time.Now()
	if len(caps) == 0 {
		caps = []string{perm.CapMarker("blobs")}
	}
	svc, _, err := signer.MintServiceToken(pluginID, caps, now, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	asr, _, err = signer.MintIdentityAssertion(pluginID, "u", projectID, "s", perm.All(), now, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return svc, asr
}

func callBlob(h *Blobs, op, key, svc, asr, contentType, body string) *httptest.ResponseRecorder {
	url := "/v1alpha1/plugin/blobs/" + op + "?key=" + key
	r := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if svc != "" {
		r.Header.Set("X-LLMObs-Service-Token", svc)
	}
	if asr != "" {
		r.Header.Set(pluginproto.IdentityAssertionHeader, asr)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	mux := http.NewServeMux()
	h.Register(mux, "/v1alpha1/plugin/blobs")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// TestBlobsRoundTrip drives put→get→delete through the double-token HTTP path.
func TestBlobsRoundTrip(t *testing.T) {
	h, signer := blobsSetup(t)
	svc, asr := blobTokens(t, signer, "acme/w", "projA")

	if rec := callBlob(h, "put", "report.json", svc, asr, "application/json", `{"ok":true}`); rec.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	rec := callBlob(h, "get", "report.json", svc, asr, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Fatalf("round-trip mismatch: %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type not preserved: %q", ct)
	}
	if rec := callBlob(h, "delete", "report.json", svc, asr, "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := callBlob(h, "get", "report.json", svc, asr, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete must be 404, got %d", rec.Code)
	}
}

// TestBlobsCrossTenantIsolation is the prove-the-negative: a blob written by one
// (plugin, project) is UNREADABLE by another plugin or another project using the SAME
// logical key — the DeriveKey seam scopes every object by the server-derived identity.
func TestBlobsCrossTenantIsolation(t *testing.T) {
	h, signer := blobsSetup(t)

	// Plugin acme/w in projA writes "shared" = "A-secret".
	aSvc, aAsr := blobTokens(t, signer, "acme/w", "projA")
	if rec := callBlob(h, "put", "shared", aSvc, aAsr, "text/plain", "A-secret"); rec.Code != http.StatusNoContent {
		t.Fatalf("A put: %d", rec.Code)
	}

	// Same plugin, DIFFERENT project (projB) — must not see A's object.
	bSvc, bAsr := blobTokens(t, signer, "acme/w", "projB")
	if rec := callBlob(h, "get", "shared", bSvc, bAsr, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-PROJECT read must be 404, got %d %q", rec.Code, rec.Body.String())
	}

	// DIFFERENT plugin, same project (projA) — must not see A's object either.
	cSvc, cAsr := blobTokens(t, signer, "evil/x", "projA")
	if rec := callBlob(h, "get", "shared", cSvc, cAsr, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-PLUGIN read must be 404, got %d %q", rec.Code, rec.Body.String())
	}

	// And a different tenant CANNOT overwrite/delete A's object (its delete hits a
	// different physical key), leaving A's data intact.
	if rec := callBlob(h, "delete", "shared", cSvc, cAsr, "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("cross-plugin delete of its own (absent) key: %d", rec.Code)
	}
	if rec := callBlob(h, "get", "shared", aSvc, aAsr, "", ""); rec.Code != http.StatusOK || rec.Body.String() != "A-secret" {
		t.Fatalf("A's object must survive another tenant's delete: %d %q", rec.Code, rec.Body.String())
	}
}

func TestBlobsRequireCapability(t *testing.T) {
	h, signer := blobsSetup(t)
	svc, asr := blobTokens(t, signer, "acme/w", "projA", perm.CapMarker("kv")) // no cap:blobs
	for _, op := range []string{"put", "get", "delete"} {
		if rec := callBlob(h, op, "k", svc, asr, "text/plain", "x"); rec.Code != http.StatusForbidden {
			t.Fatalf("%s without cap:blobs must be 403, got %d", op, rec.Code)
		}
	}
}

func TestBlobsRejectsBadKeyAndOversize(t *testing.T) {
	h, signer := blobsSetup(t)
	svc, asr := blobTokens(t, signer, "acme/w", "projA")

	// An empty logical key is a 400 (DeriveKey rejects it).
	if rec := callBlob(h, "put", "", svc, asr, "text/plain", "x"); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty key must be 400, got %d", rec.Code)
	}
	// A body over the 1 MiB test cap is a 413.
	big := strings.Repeat("a", (1<<20)+1)
	if rec := callBlob(h, "put", "big", svc, asr, "application/octet-stream", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body must be 413, got %d", rec.Code)
	}
}
