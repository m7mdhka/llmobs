package plugintoken

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestSignerConsultsRevocationForEverySignedToken is the crown proof at the signer seam:
// a token that is PERFECTLY VALID by signature + TTL (now is well inside its lifetime) is
// nonetheless DENIED when the injected revocation checker says its principal is revoked. This
// test FAILS if a Verify* ever reverts to TTL-only (ignores the checker) — which is exactly
// the #63 regression. It also proves all three signed-token classes consult the checker, that
// the checker receives the right principal keys, and that the check FAILS CLOSED on error.
func TestSignerConsultsRevocationForEverySignedToken(t *testing.T) {
	signer, err := NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	iat := time.Unix(1_700_000_000, 0)
	ttl := 5 * time.Minute
	within := iat.Add(time.Minute) // WELL within TTL — a TTL-only verifier would accept.

	// Mint one of each signed-token class.
	svc, _, err := signer.MintServiceToken("acme/dash", []string{"cap:query"}, iat, ttl)
	if err != nil {
		t.Fatal(err)
	}
	asr, _, err := signer.MintIdentityAssertion("acme/dash", "u@x", "projA", "session:u@x", nil, iat, ttl)
	if err != nil {
		t.Fatal(err)
	}
	front, _, err := signer.MintFrontendToken("acme/dash", "u@x", "projA", "frontend:u@x", nil, iat, ttl)
	if err != nil {
		t.Fatal(err)
	}

	type call struct{ jti, pluginID, email string }
	var got call
	live := true
	var checkErr error
	signer.SetRevocationChecker(func(_ context.Context, jti, pluginID, email string, issuedAt time.Time) (bool, error) {
		got = call{jti, pluginID, email}
		if !issuedAt.Equal(iat) {
			t.Errorf("checker got issuedAt %v, want token iat %v", issuedAt, iat)
		}
		return live, checkErr
	})

	ctx := context.Background()

	// --- live=true: all three verify (signature + TTL + not revoked). ---
	live, checkErr = true, nil
	if _, err := signer.VerifyServiceToken(ctx, svc, within); err != nil {
		t.Fatalf("live service token must verify: %v", err)
	}
	if got.pluginID != "acme/dash" || got.email != "" {
		t.Fatalf("service-token checker keys: got %+v, want plugin acme/dash, empty email", got)
	}
	if _, err := signer.VerifyIdentityAssertion(ctx, asr, "plugin:acme/dash", within); err != nil {
		t.Fatalf("live assertion must verify: %v", err)
	}
	if got.pluginID != "acme/dash" || got.email != "u@x" {
		t.Fatalf("assertion checker keys: got %+v, want plugin acme/dash, email u@x", got)
	}
	if _, err := signer.VerifyFrontendToken(ctx, front, within); err != nil {
		t.Fatalf("live frontend token must verify: %v", err)
	}
	if got.pluginID != "acme/dash" || got.email != "u@x" {
		t.Fatalf("frontend checker keys: got %+v, want plugin acme/dash, email u@x", got)
	}

	// --- live=false: every class is DENIED even though still within TTL (the #63 crown). ---
	live, checkErr = false, nil
	for name, verify := range map[string]func() error{
		"service":   func() error { _, e := signer.VerifyServiceToken(ctx, svc, within); return e },
		"assertion": func() error { _, e := signer.VerifyIdentityAssertion(ctx, asr, "plugin:acme/dash", within); return e },
		"frontend":  func() error { _, e := signer.VerifyFrontendToken(ctx, front, within); return e },
	} {
		if err := verify(); !errors.Is(err, ErrRevoked) {
			t.Fatalf("%s: revoked-but-within-TTL token must be ErrRevoked, got %v", name, err)
		}
	}

	// --- checker error: FAIL CLOSED (denied, not admitted). ---
	live, checkErr = true, errors.New("store down")
	if _, err := signer.VerifyFrontendToken(ctx, front, within); err == nil {
		t.Fatal("a revocation-store error must FAIL CLOSED (deny), not admit the token")
	}

	// --- expired token still fails on TTL regardless of the checker (no regression). ---
	live, checkErr = true, nil
	if _, err := signer.VerifyServiceToken(ctx, svc, iat.Add(2*ttl)); err == nil {
		t.Fatal("an expired token must still be rejected")
	}
}

// TestNilCheckerIsTTLOnly: a Signer with no revocation checker (crypto-only tests, store-less
// runs) verifies on signature + TTL alone — backward compatible.
func TestNilCheckerIsTTLOnly(t *testing.T) {
	signer, _ := NewSigner()
	iat := time.Unix(1_700_000_000, 0)
	svc, _, _ := signer.MintServiceToken("acme/dash", []string{"cap:query"}, iat, time.Minute)
	if _, err := signer.VerifyServiceToken(context.Background(), svc, iat.Add(30*time.Second)); err != nil {
		t.Fatalf("nil checker must verify a valid token on TTL alone: %v", err)
	}
}
