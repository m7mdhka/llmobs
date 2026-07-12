// Package plugintoken holds the kernel's Ed25519 signing key and wraps
// pkg/pluginproto with the kernel's clock + jti generation. The supervisor uses
// it to issue plugin service tokens at handshake, plus identity-assertion minting,
// and exposes the public key at the gateway so plugins can verify kernel-signed
// assertions (Ed25519 is asymmetric, so a plugin verifies without a shared secret).
//
// Lite-profile property: the key is generated in memory at boot and is NOT
// persisted. A kernel restart therefore rotates the key and invalidates every
// live service token — by design harmless, because tokens are short-TTL and the
// supervisor re-handshakes and re-issues on the next reconcile. A
// persisted/shared key (multi-replica, no-reissue-on-restart) is the deferred
// JWKS/rotation hardening pass.
package plugintoken

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// ErrRevoked is returned by a Verify* method when a signature-and-TTL-valid token has been
// revoked (its jti, plugin, or deriving user). It is a verification failure like any other —
// callers already map a verify error to 401/403.
var ErrRevoked = errors.New("plugintoken: credential revoked")

// RevocationCheck reports whether a stateless plugin token is still LIVE — not revoked at or
// after it was issued. Injected into the Signer so EVERY signed-token verify path (the Query
// auth seam, the plugin-primitive seam, jobs) consults revocation by construction, at the one
// chokepoint all plugin-token verification funnels through. userEmail is "" for a service
// token (deriver is the plugin, not a user). Backed by controlplane.TokenLive.
type RevocationCheck func(ctx context.Context, jti, pluginID, userEmail string, issuedAt time.Time) (bool, error)

// Signer holds the kernel's Ed25519 keypair and mints/verifies plugin tokens.
type Signer struct {
	priv   ed25519.PrivateKey
	pub    ed25519.PublicKey
	revoke RevocationCheck // nil in pure-crypto tests → TTL-only (no revocation lookup)
}

// NewSigner generates a fresh in-memory keypair.
func NewSigner() (*Signer, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("plugintoken: keygen: %w", err)
	}
	return &Signer{priv: priv, pub: pub}, nil
}

// SetRevocationChecker wires the immediate-revocation lookup. Once set, every
// Verify* additionally denies a token whose jti/plugin/user has been revoked — the OUTLIVE
// half of the derived-credential invariant. main.go injects the pool-backed checker.
func (s *Signer) SetRevocationChecker(fn RevocationCheck) { s.revoke = fn }

// checkRevoked runs the injected revocation lookup and FAILS CLOSED: a token whose liveness
// cannot be determined (store error) is denied, so an attacker cannot bypass revocation by
// disrupting the store. A nil checker (crypto-only tests) skips the lookup.
func (s *Signer) checkRevoked(ctx context.Context, jti, pluginID, userEmail string, iat int64) error {
	if s.revoke == nil {
		return nil
	}
	live, err := s.revoke(ctx, jti, pluginID, userEmail, time.Unix(iat, 0))
	if err != nil {
		return fmt.Errorf("plugintoken: revocation check failed (fail-closed): %w", err)
	}
	if !live {
		return ErrRevoked
	}
	return nil
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

// VerifyServiceToken verifies a service token against the current key, then denies it if the
// plugin (or the token's jti) has been revoked. A service token has no deriving
// user, so the user principal is empty.
func (s *Signer) VerifyServiceToken(ctx context.Context, token string, now time.Time) (pluginproto.ServiceTokenClaims, error) {
	c, err := pluginproto.VerifyServiceToken(s.pub, token, now)
	if err != nil {
		return c, err
	}
	if err := s.checkRevoked(ctx, c.Jti, c.PluginID, "", c.Iat); err != nil {
		return c, err
	}
	return c, nil
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
// plugin's `plugin:{id}` audience) against the current key, then denies it if the deriving
// user (Sub = email), the plugin (audience), or the jti has been revoked.
func (s *Signer) VerifyIdentityAssertion(ctx context.Context, token, expectedAud string, now time.Time) (pluginproto.IdentityAssertionClaims, error) {
	c, err := pluginproto.VerifyIdentityAssertion(s.pub, token, expectedAud, now)
	if err != nil {
		return c, err
	}
	pluginID, _ := pluginproto.PluginIDFromSubject(c.Aud)
	if err := s.checkRevoked(ctx, c.Jti, pluginID, c.Sub, c.Iat); err != nil {
		return c, err
	}
	return c, nil
}

// MintFrontendToken issues a frontend token — an identity assertion whose scopes
// are already the plugin-grant ∩ user ∩ project intersection, stamped with the
// signed PurposeFrontend marker so it cannot be confused with a proxy/jobs assertion.
func (s *Signer) MintFrontendToken(pluginID, subject, projectID, actor string, scopes []string, now time.Time, ttl time.Duration) (string, pluginproto.IdentityAssertionClaims, error) {
	jti, err := randomJTI()
	if err != nil {
		return "", pluginproto.IdentityAssertionClaims{}, fmt.Errorf("plugintoken: jti: %w", err)
	}
	return pluginproto.MintFrontendToken(s.priv, pluginID, subject, projectID, actor, scopes, now, ttl, jti)
}

// VerifyFrontendToken verifies a frontend token against the current key, requiring the
// signed PurposeFrontend marker (rejecting proxy/jobs assertions), then denies it if the
// deriving user (Sub = email), the plugin (audience), or the jti has been revoked. This
// is what stops a revoked user's browser frontend token from working out its 5-minute TTL.
func (s *Signer) VerifyFrontendToken(ctx context.Context, token string, now time.Time) (pluginproto.IdentityAssertionClaims, error) {
	c, err := pluginproto.VerifyFrontendToken(s.pub, token, now)
	if err != nil {
		return c, err
	}
	pluginID, _ := pluginproto.PluginIDFromSubject(c.Aud)
	if err := s.checkRevoked(ctx, c.Jti, pluginID, c.Sub, c.Iat); err != nil {
		return c, err
	}
	return c, nil
}
