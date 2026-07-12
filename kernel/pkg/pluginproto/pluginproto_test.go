package pluginproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"reflect"
	"testing"
	"time"
)

func keypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return pub, priv
}

func TestServiceTokenRoundTrip(t *testing.T) {
	pub, priv := keypair(t)
	now := time.Unix(1_700_000_000, 0)
	tok, claims, err := MintServiceToken(priv, "acme/widget", []string{"query", "traces:read.metadata"}, now, 5*time.Minute, "jti-1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyServiceToken(pub, tok, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.PluginID != "acme/widget" || got.Sub != "plugin:acme/widget" || got.Aud != KernelAudience {
		t.Fatalf("claims wrong: %+v", got)
	}
	if !reflect.DeepEqual(got.Scopes, claims.Scopes) {
		t.Fatalf("scopes lost: %v vs %v", got.Scopes, claims.Scopes)
	}
}

func TestServiceTokenExpiry(t *testing.T) {
	pub, priv := keypair(t)
	now := time.Unix(1_700_000_000, 0)
	tok, _, _ := MintServiceToken(priv, "acme/widget", nil, now, time.Minute, "jti-1")
	if _, err := VerifyServiceToken(pub, tok, now.Add(2*time.Minute)); err != ErrExpired {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

func TestTamperFailsSignature(t *testing.T) {
	pub, priv := keypair(t)
	now := time.Unix(1_700_000_000, 0)
	tok, _, _ := MintServiceToken(priv, "acme/widget", []string{"query"}, now, time.Minute, "jti-1")
	// Flip a byte in the payload segment.
	b := []byte(tok)
	for i := range b {
		if b[i] == '.' {
			b[i+1] ^= 0x01
			break
		}
	}
	if _, err := VerifyServiceToken(pub, string(b), now); err == nil {
		t.Fatal("tampered token must not verify")
	}
}

func TestWrongKeyFails(t *testing.T) {
	_, priv := keypair(t)
	otherPub, _ := keypair(t)
	now := time.Unix(1_700_000_000, 0)
	tok, _, _ := MintServiceToken(priv, "acme/widget", nil, now, time.Minute, "jti-1")
	if _, err := VerifyServiceToken(otherPub, tok, now); err != ErrBadSignature {
		t.Fatalf("want ErrBadSignature, got %v", err)
	}
}

func TestIdentityAssertionAudienceConfusion(t *testing.T) {
	pub, priv := keypair(t)
	now := time.Unix(1_700_000_000, 0)
	// Minted for plugin A.
	tok, _, _ := MintIdentityAssertion(priv, "acme/a", "user-1", "proj_default", "session:u@x", []string{"query"}, now, time.Minute, "jti-1")
	// Plugin B must reject it (audience binding).
	if _, err := VerifyIdentityAssertion(pub, tok, PluginSubject("acme/b"), now); err != ErrBadAudience {
		t.Fatalf("cross-plugin assertion must fail audience, got %v", err)
	}
	// Plugin A accepts it.
	got, err := VerifyIdentityAssertion(pub, tok, PluginSubject("acme/a"), now)
	if err != nil {
		t.Fatalf("own-audience assertion should verify: %v", err)
	}
	if got.ProjectID != "proj_default" || got.Sub != "user-1" {
		t.Fatalf("claims wrong: %+v", got)
	}
}

// TestFrontendTokenClassSeparation proves the token-class marker: a frontend
// token verifies only via VerifyFrontendToken, and the two verifiers reject each
// other's class — so a proxy/jobs-minted identity assertion (no purpose) cannot be
// replayed on the frontend seam, and a frontend token cannot be replayed on the
// backend identity path.
func TestFrontendTokenClassSeparation(t *testing.T) {
	pub, priv := keypair(t)
	now := time.Unix(1_700_000_000, 0)

	frontendTok, _, _ := MintFrontendToken(priv, "acme/a", "u@x", "proj_default", "frontend:u@x", []string{"traces:read.metadata"}, now, time.Minute, "jti-f")
	proxyTok, _, _ := MintIdentityAssertion(priv, "acme/a", "u@x", "proj_default", "session:u@x", []string{"traces:read.metadata"}, now, time.Minute, "jti-p")

	// Frontend token: accepted by the frontend verifier, carries the marker.
	fc, err := VerifyFrontendToken(pub, frontendTok, now)
	if err != nil {
		t.Fatalf("frontend token must verify on its own path: %v", err)
	}
	if fc.Purpose != PurposeFrontend {
		t.Fatalf("frontend token must carry purpose marker, got %q", fc.Purpose)
	}
	// ...and REJECTED on the generic identity-assertion path (even for its own aud).
	if _, err := VerifyIdentityAssertion(pub, frontendTok, PluginSubject("acme/a"), now); err != ErrBadPurpose {
		t.Fatalf("frontend token must be rejected as an identity assertion, got %v", err)
	}

	// Proxy/jobs assertion: accepted on the identity path, REJECTED on the frontend
	// seam (this is the escalation the review caught).
	if _, err := VerifyIdentityAssertion(pub, proxyTok, PluginSubject("acme/a"), now); err != nil {
		t.Fatalf("proxy assertion must verify on the identity path: %v", err)
	}
	if _, err := VerifyFrontendToken(pub, proxyTok, now); err != ErrBadPurpose {
		t.Fatalf("proxy assertion must be rejected on the frontend seam, got %v", err)
	}
}

func TestMalformedToken(t *testing.T) {
	pub, _ := keypair(t)
	for _, tok := range []string{"", "nope", "v2.a.b", "v1.@@@.bbb", "v1.only-two"} {
		if _, err := VerifyServiceToken(pub, tok, time.Now()); err == nil {
			t.Fatalf("malformed %q must not verify", tok)
		}
	}
}

func TestHandshakeCheck(t *testing.T) {
	ok := Info{ID: "acme/widget", Version: "1.0.0", PluginAPIVersion: "v1alpha1", Capabilities: []string{"query", "kv"}}
	if err := ok.Check("acme/widget", []string{"query", "kv", "surface"}); err != nil {
		t.Fatalf("valid handshake rejected: %v", err)
	}
	// id mismatch
	if err := ok.Check("acme/other", []string{"query", "kv"}); err == nil {
		t.Fatal("id mismatch must fail")
	}
	// capability over-reach (claims more than manifest granted)
	if err := ok.Check("acme/widget", []string{"query"}); err == nil {
		t.Fatal("capability over-reach must fail")
	}
	// unsupported protocol
	bad := Info{ID: "acme/widget", PluginAPIVersion: "v9", Capabilities: nil}
	if err := bad.Check("acme/widget", nil); err == nil {
		t.Fatal("unsupported protocol must fail")
	}
}

func TestWatermarkStaleness(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	fresh := Health{Live: true, Ready: true, Watermark: &Watermark{LastProgressUnix: now.Add(-5 * time.Second).Unix()}}
	if fresh.StaleWatermark(now, 30*time.Second) {
		t.Fatal("fresh watermark should not be stale")
	}
	stale := Health{Live: true, Ready: true, Watermark: &Watermark{LastProgressUnix: now.Add(-60 * time.Second).Unix()}}
	if !stale.StaleWatermark(now, 30*time.Second) {
		t.Fatal("stale watermark should be flagged")
	}
	// No watermark, or no budget => never stale on this basis (busy long job safe).
	if (Health{Ready: true}).StaleWatermark(now, 30*time.Second) {
		t.Fatal("absent watermark must not be considered stale")
	}
	if fresh.StaleWatermark(now, 0) {
		t.Fatal("zero budget disables the watermark check")
	}
}

func TestIntersect(t *testing.T) {
	got := Intersect([]string{"query", "traces:read.metadata", "kv"}, []string{"kv", "query", "traces:read.payloads"})
	want := []string{"query", "kv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("intersect = %v, want %v", got, want)
	}
	if len(Intersect(nil, []string{"query"})) != 0 {
		t.Fatal("empty ∩ anything is empty")
	}
}
