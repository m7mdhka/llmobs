// Package plugintoken holds the kernel's Ed25519 signing key and wraps
// pkg/pluginproto with the kernel's clock + jti generation. The supervisor uses
// it to issue plugin service tokens at handshake (H2); H3 extends it with
// identity-assertion minting and exposes the public key at the gateway so plugins
// can verify kernel-signed assertions.
//
// Lite-profile property: the key is generated in memory at boot and is NOT
// persisted. A kernel restart therefore rotates the key and invalidates every
// live service token — by design harmless, because tokens are short-TTL and the
// supervisor re-handshakes and re-issues on the next reconcile (ADR-0023). A
// persisted/shared key (multi-replica, no-reissue-on-restart) is the deferred
// JWKS/rotation hardening pass.
package plugintoken

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// Signer holds the kernel's Ed25519 keypair and mints/verifies plugin tokens.
type Signer struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

// NewSigner generates a fresh in-memory keypair.
func NewSigner() (*Signer, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("plugintoken: keygen: %w", err)
	}
	return &Signer{priv: priv, pub: pub}, nil
}

// Public returns the kernel public key plugins use to verify kernel-minted tokens.
func (s *Signer) Public() ed25519.PublicKey { return s.pub }

func randomJTI() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// MintServiceToken issues a short-TTL service token for a plugin with the given
// approved scopes.
func (s *Signer) MintServiceToken(pluginID string, scopes []string, now time.Time, ttl time.Duration) (string, pluginproto.ServiceTokenClaims, error) {
	jti, err := randomJTI()
	if err != nil {
		return "", pluginproto.ServiceTokenClaims{}, fmt.Errorf("plugintoken: jti: %w", err)
	}
	return pluginproto.MintServiceToken(s.priv, pluginID, scopes, now, ttl, jti)
}

// VerifyServiceToken verifies a service token against the current key.
func (s *Signer) VerifyServiceToken(token string, now time.Time) (pluginproto.ServiceTokenClaims, error) {
	return pluginproto.VerifyServiceToken(s.pub, token, now)
}

// MintIdentityAssertion issues a per-request identity assertion for a plugin
// audience carrying the user's effective (canonical) permissions. Short-TTL,
// audience-bound; the gateway proxy injects it and strips the session cookie.
func (s *Signer) MintIdentityAssertion(pluginID, subject, projectID, actor string, scopes []string, now time.Time, ttl time.Duration) (string, pluginproto.IdentityAssertionClaims, error) {
	jti, err := randomJTI()
	if err != nil {
		return "", pluginproto.IdentityAssertionClaims{}, fmt.Errorf("plugintoken: jti: %w", err)
	}
	return pluginproto.MintIdentityAssertion(s.priv, pluginID, subject, projectID, actor, scopes, now, ttl, jti)
}

// VerifyIdentityAssertion verifies an assertion bound to expectedAud (the calling
// plugin's `plugin:{id}` audience) against the current key.
func (s *Signer) VerifyIdentityAssertion(token, expectedAud string, now time.Time) (pluginproto.IdentityAssertionClaims, error) {
	return pluginproto.VerifyIdentityAssertion(s.pub, token, expectedAud, now)
}
