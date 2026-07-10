// Package pluginproto is the public, plugin-facing contract library for the
// Tier-3 plugin protocol (api/plugin/v1alpha1, ADR-0023). A plugin backend — and
// every first-party plugin, per the dogfood rule (ADR-0002) — imports THIS and
// only this from the kernel: the handshake/health types, the service-token and
// identity-assertion claim types, and the stdlib-only Sign/Verify helpers.
//
// It deliberately depends on nothing kernel-internal and nothing outside the
// standard library (Ed25519 for signatures — no JWT dependency on the hot path,
// go-style). The kernel mints tokens with these helpers; a plugin verifies a
// kernel-minted identity assertion with the same helpers and the kernel's public
// key (delivered out of band for v1alpha1; JWKS/rotation deferred, ADR-0023).
//
// The JSON Schemas in api/plugin/v1alpha1 are the contract; the types here are
// kept in sync as DX. Runtime wiring (supervisor, executor, proxy, intersection)
// lands in later Arc-H PRs; this package is types + crypto helpers only.
package pluginproto

// Protocol identity constants shared by both sides of the handshake.
const (
	// ProtocolVersion is the plugin-protocol maturity this library implements.
	ProtocolVersion = "v1alpha1"

	// IssuerKernel is the `iss` claim on every kernel-minted token.
	IssuerKernel = "llmobs-kernel"
	// KernelAudience is the `aud` a service token is bound to (verified on the
	// return path into the kernel).
	KernelAudience = "llmobs-kernel"

	// IdentityAssertionHeader carries the per-request identity assertion the
	// gateway injects on proxied calls to a plugin backend.
	IdentityAssertionHeader = "X-LLMObs-Identity-Assertion"

	// FrontendTokenHeader carries a J1 plugin frontend token (a purpose-marked,
	// pre-intersected identity assertion) on a plugin frontend's Query API calls.
	FrontendTokenHeader = "X-LLMObs-Frontend-Token"

	// DefaultInfoPath / DefaultHealthPath are the well-known plugin endpoints the
	// supervisor calls when the manifest does not override them.
	DefaultInfoPath   = "/plugin/v1/info"
	DefaultHealthPath = "/plugin/v1/health"
	// DefaultTokenPath is the well-known endpoint the kernel PUSHES the plugin's
	// service token to at handshake completion + on refresh (H7c). Delivery is
	// kernel-initiated to the plugin's own registered URL — there is no plugin-pull
	// path, so "obtain another plugin's token" is not an expressible operation.
	DefaultTokenPath = "/plugin/v1/token"
)

// TokenDelivery is the body the kernel POSTs to a plugin's token endpoint. The
// plugin stores the token and presents it on calls back to the kernel; it MUST
// treat a delivery as authoritative only over its operator-configured URL.
type TokenDelivery struct {
	ServiceToken string `json:"serviceToken"`
	ExpiresUnix  int64  `json:"expiresUnix"`
}

// SupportedProtocols is the set of plugin-protocol maturities this kernel can run.
var SupportedProtocols = []string{"v1alpha1"}

// ProtocolSupported reports whether the kernel can run a plugin advertising v.
func ProtocolSupported(v string) bool {
	for _, s := range SupportedProtocols {
		if s == v {
			return true
		}
	}
	return false
}

// PluginSubject renders the `plugin:{id}` subject/audience form.
func PluginSubject(pluginID string) string { return "plugin:" + pluginID }
