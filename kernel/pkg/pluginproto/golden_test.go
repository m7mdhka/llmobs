package pluginproto

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"
)

// TestGoldenAssertionVector pins the exact cross-language token bytes for a
// deterministic key + claims. The committed Python interop test
// (plugins/langfuse-compat/backend/interop_test.py) verifies THIS same vector, so
// any drift in the token format (field order, base64 variant, signing input) that
// would break a non-Go backend fails here in Go CI first. Ed25519 signing is
// deterministic (RFC 8032), so the signature is reproducible.
func TestGoldenAssertionVector(t *testing.T) {
	seed := make([]byte, 32) // all-zero seed
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)

	const wantPubHex = "3b6a27bcceb6a42d62a3a8d02a6f0d73653215771de243a63ac048a18b59da29"
	if got := hex.EncodeToString(pub); got != wantPubHex {
		t.Fatalf("public key drift: %s", got)
	}

	iat := time.Unix(1_700_000_000, 0)
	tok, _, err := MintIdentityAssertion(priv, "acme/w", "user-1", "projA", "session:u@x",
		[]string{"traces:read.metadata"}, iat, 5*time.Minute, "jti-1")
	if err != nil {
		t.Fatal(err)
	}
	const want = "v1.eyJpc3MiOiJsbG1vYnMta2VybmVsIiwic3ViIjoidXNlci0xIiwiYXVkIjoicGx1Z2luOmFjbWUvdyIsImlhdCI6MTcwMDAwMDAwMCwiZXhwIjoxNzAwMDAwMzAwLCJqdGkiOiJqdGktMSIsInByb2plY3RJZCI6InByb2pBIiwiYWN0b3IiOiJzZXNzaW9uOnVAeCIsInNjb3BlcyI6WyJ0cmFjZXM6cmVhZC5tZXRhZGF0YSJdfQ.lgNwOJeZ78MGb3tcLNJJErDEkBuBUEjD4gcETZWL8XKLjCWVOP5WjAMTQd4gPrJeAyYqquxpd-YItGgkvSeFCA"
	if tok != want {
		t.Fatalf("token vector drift:\n got: %s\nwant: %s", tok, want)
	}
	// Sanity: it verifies with the deterministic public key.
	if _, err := VerifyIdentityAssertion(pub, tok, PluginSubject("acme/w"), iat.Add(time.Minute)); err != nil {
		t.Fatalf("golden token must verify: %v", err)
	}
}
