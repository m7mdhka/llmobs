package controlplane

import "testing"

func TestArgon2idRoundTrip(t *testing.T) {
	secret := "sk-1234567890abcdef"
	enc, err := hashArgon2id(secret)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	ok, err := verifyArgon2id(secret, enc)
	if err != nil || !ok {
		t.Fatalf("verify should accept the right secret: ok=%v err=%v", ok, err)
	}
	bad, err := verifyArgon2id("sk-wrong", enc)
	if err != nil {
		t.Fatalf("verify wrong secret errored: %v", err)
	}
	if bad {
		t.Fatal("verify accepted the wrong secret")
	}
}

// Each hash uses a fresh salt, so encodings differ even for the same secret.
func TestArgon2idSaltIsRandom(t *testing.T) {
	a, _ := hashArgon2id("same-secret")
	b, _ := hashArgon2id("same-secret")
	if a == b {
		t.Fatal("two hashes of the same secret are identical — salt not random")
	}
}

// The selector is deterministic (so it can index) but differs per secret.
func TestSelectorDeterministicAndDistinct(t *testing.T) {
	if selector("k1") != selector("k1") {
		t.Fatal("selector must be deterministic for lookup")
	}
	if selector("k1") == selector("k2") {
		t.Fatal("distinct secrets must have distinct selectors")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "not-a-hash", "$argon2id$bogus", "$argon2i$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA"} {
		if _, err := verifyArgon2id("x", bad); err == nil {
			t.Fatalf("expected error for malformed hash %q", bad)
		}
	}
}
