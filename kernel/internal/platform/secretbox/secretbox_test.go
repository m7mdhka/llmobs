package secretbox

import (
	"bytes"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	b, err := NewRandom()
	if err != nil {
		t.Fatal(err)
	}
	pt := []byte("sk-super-secret")
	ct, nonce, err := b.Seal(pt)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, pt) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := b.Open(ct, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("round-trip mismatch: %q", got)
	}
}

func TestTamperDetected(t *testing.T) {
	b, _ := NewRandom()
	ct, nonce, _ := b.Seal([]byte("value"))
	ct[0] ^= 0x01 // flip a bit
	if _, err := b.Open(ct, nonce); err == nil {
		t.Fatal("tampered ciphertext must fail authentication")
	}
}

func TestWrongKeyFails(t *testing.T) {
	b1, _ := NewRandom()
	b2, _ := NewRandom()
	ct, nonce, _ := b1.Seal([]byte("value"))
	if _, err := b2.Open(ct, nonce); err == nil {
		t.Fatal("decryption under a different key must fail")
	}
}

func TestBadNonce(t *testing.T) {
	b, _ := NewRandom()
	ct, _, _ := b.Seal([]byte("value"))
	if _, err := b.Open(ct, []byte("short")); err == nil {
		t.Fatal("bad nonce length must error")
	}
}
