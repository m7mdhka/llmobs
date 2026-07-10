package pluginapi

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestKernelKeyPublishesVerifiableKey: the endpoint publishes the raw Ed25519
// public key (hex + base64) a plugin backend needs to verify assertions (finding
// #2). Unauthenticated by design (the key is public); only GET.
func TestKernelKeyPublishesVerifiableKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewKernelKey(pub).Register(mux, "/v1alpha1/plugin/kernel-key")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1alpha1/plugin/kernel-key", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("kernel-key GET should be 200, got %d", rec.Code)
	}
	var body struct {
		Algorithm    string `json:"algorithm"`
		PublicKeyHex string `json:"public_key_hex"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Algorithm != "ed25519" {
		t.Fatalf("algorithm = %q", body.Algorithm)
	}
	// The published key round-trips to the real public key.
	got, err := hex.DecodeString(body.PublicKeyHex)
	if err != nil || len(got) != ed25519.PublicKeySize || string(got) != string(pub) {
		t.Fatalf("published key does not match: %v", err)
	}

	// POST is rejected.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1alpha1/plugin/kernel-key", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST should be 405, got %d", rec.Code)
	}
}
