package controlplane

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// API-key secrets are high-entropy random tokens, but we hash them with argon2id
// so a database leak never yields a usable credential even under future analysis.
// argon2id uses a per-key random salt, which breaks an O(1) index lookup — so we
// pair it with a deterministic SHA-256 *selector* stored in lookup_hash: find the
// row by selector, then verify the secret against the argon2id hash. The selector
// is a hash of a 192-bit secret, so it leaks nothing brute-forceable on its own.

// argon2id parameters (OWASP second-recommended profile). Encoded in each hash so
// they can be tuned later without invalidating existing keys.
const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// selector is the deterministic lookup key for a secret (indexed, not a verifier).
func selector(secret string) string {
	sum := sha256.Sum256([]byte("llmobs-key-selector:" + secret))
	return hex.EncodeToString(sum[:])
}

// hashArgon2id returns a PHC-encoded argon2id hash with a fresh random salt.
func hashArgon2id(secret string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

var errBadHash = errors.New("malformed argon2id hash")

// verifyArgon2id reports whether secret matches a PHC-encoded argon2id hash,
// using a constant-time comparison.
func verifyArgon2id(secret, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var mem, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return false, errBadHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(secret), salt, t, mem, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
