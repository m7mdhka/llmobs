package pluginproto

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// tokenScheme is the version prefix of the compact token format
// "v1.<base64url(claimsJSON)>.<base64url(ed25519sig)>".
const tokenScheme = "v1"

// Token/verification errors. Callers distinguish these to return the right status
// (bad signature/audience/issuer => reject; expired => refresh).
var (
	ErrMalformedToken = errors.New("pluginproto: malformed token")
	ErrBadSignature   = errors.New("pluginproto: bad signature")
	ErrExpired        = errors.New("pluginproto: token expired")
	ErrBadAudience    = errors.New("pluginproto: wrong audience")
	ErrBadIssuer      = errors.New("pluginproto: wrong issuer")
	ErrBadPurpose     = errors.New("pluginproto: wrong token purpose")
)

// PurposeFrontend marks an identity assertion minted as a plugin FRONTEND token:
// its scopes were already intersected (plugin-grant ∩ user ∩ project) at
// mint, so the Query API frontend-token case may use them directly. This marker is
// SIGNED — it distinguishes a frontend token from a proxy/jobs-minted identity
// assertion, which carry un-intersected (full role) scopes and MUST NOT be usable
// on the frontend seam. Proxy/jobs mints leave Purpose empty; only MintFrontendToken
// sets this, and only VerifyFrontendToken accepts it.
const PurposeFrontend = "frontend"

// ServiceTokenClaims proves which plugin is calling and what it may do
// (api/plugin/v1alpha1/service-token.schema.json). The plugin half of the
// double-token intersection.
type ServiceTokenClaims struct {
	Iss      string   `json:"iss"`
	Sub      string   `json:"sub"`
	Aud      string   `json:"aud"`
	Iat      int64    `json:"iat"`
	Exp      int64    `json:"exp"`
	Jti      string   `json:"jti"`
	PluginID string   `json:"pluginId"`
	Scopes   []string `json:"scopes"`
}

// IdentityAssertionClaims proves who the end user is and what they may see
// (api/plugin/v1alpha1/identity-assertion.schema.json). The user half of the
// intersection; audience-bound to a single plugin.
type IdentityAssertionClaims struct {
	Iss       string   `json:"iss"`
	Sub       string   `json:"sub"`
	Aud       string   `json:"aud"`
	Iat       int64    `json:"iat"`
	Exp       int64    `json:"exp"`
	Jti       string   `json:"jti"`
	ProjectID string   `json:"projectId"`
	Actor     string   `json:"actor,omitempty"`
	Scopes    []string `json:"scopes"`
	// Purpose distinguishes token classes that share this claim shape. Empty for the
	// per-request proxy assertion and the jobs system assertion (both carry
	// un-intersected user/plugin scopes, confined downstream by the service-token
	// intersection). Set to PurposeFrontend ONLY for frontend tokens, whose scopes
	// are pre-intersected — so the frontend seam can accept them and reject the
	// others. Additive/optional (v1alpha1).
	Purpose string `json:"purpose,omitempty"`
}

var b64 = base64.RawURLEncoding

// Sign marshals claims to JSON and returns a compact Ed25519-signed token. The
// signing input is "v1.<base64url(claims)>" so the scheme is covered by the
// signature.
func Sign(priv ed25519.PrivateKey, claims any) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("pluginproto: marshal claims: %w", err)
	}
	signingInput := tokenScheme + "." + b64.EncodeToString(payload)
	sig := ed25519.Sign(priv, []byte(signingInput))
	return signingInput + "." + b64.EncodeToString(sig), nil
}

// verify checks the token's structure and Ed25519 signature and unmarshals the
// claims into out. It does NOT check iss/aud/exp — the typed verifiers below do.
func verify(pub ed25519.PublicKey, token string, out any) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != tokenScheme {
		return ErrMalformedToken
	}
	payload, err := b64.DecodeString(parts[1])
	if err != nil {
		return ErrMalformedToken
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return ErrMalformedToken
	}
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return ErrBadSignature
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return ErrMalformedToken
	}
	return nil
}

// MintServiceToken issues a service token for a plugin with the given approved
// scopes. jti must be unique (the caller supplies it — this package has no clock
// or randomness of its own).
func MintServiceToken(priv ed25519.PrivateKey, pluginID string, scopes []string, iat time.Time, ttl time.Duration, jti string) (string, ServiceTokenClaims, error) {
	c := ServiceTokenClaims{
		Iss: IssuerKernel, Sub: PluginSubject(pluginID), Aud: KernelAudience,
		Iat: iat.Unix(), Exp: iat.Add(ttl).Unix(), Jti: jti, PluginID: pluginID, Scopes: scopes,
	}
	t, err := Sign(priv, c)
	return t, c, err
}

// VerifyServiceToken checks a service token presented to the kernel: signature,
// issuer, kernel audience, and expiry. Returns the claims on success.
func VerifyServiceToken(pub ed25519.PublicKey, token string, now time.Time) (ServiceTokenClaims, error) {
	var c ServiceTokenClaims
	if err := verify(pub, token, &c); err != nil {
		return c, err
	}
	if c.Iss != IssuerKernel {
		return c, ErrBadIssuer
	}
	if c.Aud != KernelAudience {
		return c, ErrBadAudience
	}
	if now.Unix() >= c.Exp {
		return c, ErrExpired
	}
	return c, nil
}

// MintIdentityAssertion issues a per-request assertion for a specific plugin
// audience carrying the user's effective scopes. jti must be unique.
func MintIdentityAssertion(priv ed25519.PrivateKey, pluginID, subject, projectID, actor string, scopes []string, iat time.Time, ttl time.Duration, jti string) (string, IdentityAssertionClaims, error) {
	c := IdentityAssertionClaims{
		Iss: IssuerKernel, Sub: subject, Aud: PluginSubject(pluginID),
		Iat: iat.Unix(), Exp: iat.Add(ttl).Unix(), Jti: jti,
		ProjectID: projectID, Actor: actor, Scopes: scopes,
	}
	t, err := Sign(priv, c)
	return t, c, err
}

// VerifyIdentityAssertion checks an assertion: signature, issuer, expiry, and —
// when expectedAud is non-empty — that it was minted for this plugin's audience
// (`plugin:{id}`). A plugin MUST pass its own audience so an assertion minted for
// another plugin is rejected (token-confusion defence).
func VerifyIdentityAssertion(pub ed25519.PublicKey, token, expectedAud string, now time.Time) (IdentityAssertionClaims, error) {
	var c IdentityAssertionClaims
	if err := verify(pub, token, &c); err != nil {
		return c, err
	}
	if c.Iss != IssuerKernel {
		return c, ErrBadIssuer
	}
	if expectedAud != "" && c.Aud != expectedAud {
		return c, ErrBadAudience
	}
	// A frontend token (pre-intersected scopes) is a distinct class and must not be
	// accepted on the proxy/backend identity path — keep the classes cleanly split.
	if c.Purpose == PurposeFrontend {
		return c, ErrBadPurpose
	}
	if now.Unix() >= c.Exp {
		return c, ErrExpired
	}
	return c, nil
}

// MintFrontendToken issues a frontend token: an identity assertion whose scopes
// are ALREADY the plugin-grant ∩ user ∩ project intersection, stamped with a signed
// PurposeFrontend marker. Only this mint sets the marker, and only
// VerifyFrontendToken accepts it — so a proxy/jobs-minted assertion (un-intersected
// full scopes, no purpose) can never be replayed onto the frontend seam. jti must
// be unique.
func MintFrontendToken(priv ed25519.PrivateKey, pluginID, subject, projectID, actor string, scopes []string, iat time.Time, ttl time.Duration, jti string) (string, IdentityAssertionClaims, error) {
	c := IdentityAssertionClaims{
		Iss: IssuerKernel, Sub: subject, Aud: PluginSubject(pluginID),
		Iat: iat.Unix(), Exp: iat.Add(ttl).Unix(), Jti: jti,
		ProjectID: projectID, Actor: actor, Scopes: scopes, Purpose: PurposeFrontend,
	}
	t, err := Sign(priv, c)
	return t, c, err
}

// VerifyFrontendToken checks a frontend token: signature, issuer, the signed
// PurposeFrontend marker (this is what rejects proxy/jobs assertions), and expiry.
// There is no per-plugin audience check — the frontend seam has no service token to
// name a specific plugin, and the scopes were already bounded at mint.
func VerifyFrontendToken(pub ed25519.PublicKey, token string, now time.Time) (IdentityAssertionClaims, error) {
	var c IdentityAssertionClaims
	if err := verify(pub, token, &c); err != nil {
		return c, err
	}
	if c.Iss != IssuerKernel {
		return c, ErrBadIssuer
	}
	if c.Purpose != PurposeFrontend {
		return c, ErrBadPurpose
	}
	if now.Unix() >= c.Exp {
		return c, ErrExpired
	}
	return c, nil
}

// Intersect returns the scopes present in BOTH a and b, order-preserving by a and
// de-duplicated. The kernel computes effective plugin access as the intersection
// of the service-token scopes, the identity-assertion scopes, and project scope;
// this is the building block. Nil/empty inputs yield an empty intersection.
func Intersect(a, b []string) []string {
	set := make(map[string]struct{}, len(b))
	for _, s := range b {
		set[s] = struct{}{}
	}
	var out []string
	seen := make(map[string]struct{}, len(a))
	for _, s := range a {
		if _, ok := set[s]; !ok {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
