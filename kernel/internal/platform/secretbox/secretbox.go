// Package secretbox is envelope encryption for plugin secrets (H4), stdlib-only
// (AES-256-GCM). The kernel holds a master key; for the lite profile it is
// generated in memory at boot (like the plugin signing key). A KMS-backed key
// provider slots in for scale behind the same Box surface — that is the deferred
// hardening seam (ADR-0023). GCM authenticates the ciphertext, so tampering is
// detected on Open.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// Box seals and opens secret values under a 256-bit master key.
type Box struct {
	gcm cipher.AEAD
}

// New builds a Box from a 32-byte key.
func New(key [32]byte) (*Box, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("secretbox: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretbox: gcm: %w", err)
	}
	return &Box{gcm: gcm}, nil
}

// NewRandom generates a fresh in-memory master key (lite profile).
func NewRandom() (*Box, error) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, fmt.Errorf("secretbox: keygen: %w", err)
	}
	return New(key)
}

// Seal encrypts plaintext, returning ciphertext and the per-value nonce.
func (b *Box) Seal(plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, b.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("secretbox: nonce: %w", err)
	}
	return b.gcm.Seal(nil, nonce, plaintext, nil), nonce, nil
}

// Open decrypts ciphertext with its nonce, verifying the GCM tag.
func (b *Box) Open(ciphertext, nonce []byte) ([]byte, error) {
	if len(nonce) != b.gcm.NonceSize() {
		return nil, errors.New("secretbox: bad nonce length")
	}
	pt, err := b.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("secretbox: decryption failed") // never echoes plaintext
	}
	return pt, nil
}
