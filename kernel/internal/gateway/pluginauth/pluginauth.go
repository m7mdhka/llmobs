// Package pluginauth authorizes a plugin backend calling a kernel primitive
// endpoint (kv, secrets, store, events, jobs). It verifies the plugin double
// token — the service token (which plugin) and the identity assertion (which user
// + project, audience-bound to this plugin) — and gates on the required
// capability. The caller is scoped to the assertion's project; a plugin can never
// pick a different tenant, and a plugin-supplied identity is never trusted.
//
// This is the primitive-endpoint analogue of query.authPlugin: primitives operate
// on the plugin's OWN data (kv/secrets/store), so they gate on the capability and
// scope by project — there is no telemetry-permission intersection to compute.
package pluginauth

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// Caller is an authorized plugin request, scoped to a tenant.
type Caller struct {
	PluginID  string
	ProjectID string
	Actor     string
}

// Authorizer verifies plugin double tokens against the kernel signer.
type Authorizer struct {
	signer *plugintoken.Signer
	now    func() time.Time
}

// New builds an Authorizer. now may be nil (defaults to time.Now).
func New(signer *plugintoken.Signer, now func() time.Time) *Authorizer {
	if now == nil {
		now = time.Now
	}
	return &Authorizer{signer: signer, now: now}
}

// Require verifies the double token and the capability. On success it returns the
// caller and http.StatusOK; otherwise a status code + error the handler surfaces.
func (a *Authorizer) Require(r *http.Request, capability string) (Caller, int, error) {
	if a.signer == nil {
		return Caller{}, http.StatusServiceUnavailable, errors.New("plugin auth unavailable")
	}
	svc := r.Header.Get("X-LLMObs-Service-Token")
	asr := r.Header.Get(pluginproto.IdentityAssertionHeader)
	if svc == "" || asr == "" {
		return Caller{}, http.StatusUnauthorized, errors.New("service token + identity assertion required")
	}
	now := a.now()
	stc, err := a.signer.VerifyServiceToken(r.Context(), svc, now)
	if err != nil {
		return Caller{}, http.StatusUnauthorized, fmt.Errorf("invalid service token: %w", err)
	}
	// The assertion MUST be bound to THIS plugin's audience (token-confusion defence).
	ac, err := a.signer.VerifyIdentityAssertion(r.Context(), asr, pluginproto.PluginSubject(stc.PluginID), now)
	if err != nil {
		return Caller{}, http.StatusUnauthorized, fmt.Errorf("invalid identity assertion: %w", err)
	}
	caps, _ := perm.SplitCapsAndPerms(stc.Scopes)
	if !perm.Has(caps, perm.CapMarker(capability)) {
		return Caller{}, http.StatusForbidden, fmt.Errorf("plugin lacks capability %q", capability)
	}
	return Caller{PluginID: stc.PluginID, ProjectID: ac.ProjectID, Actor: ac.Actor}, http.StatusOK, nil
}

// RequireFrontend authorizes a plugin FRONTEND call (J2) using the J1 frontend
// token — the only credential a pure-frontend plugin holds. The plugin id comes from
// the token's audience and the project from its claims, so a frontend can only reach
// its OWN data in its OWN tenant; the request body cannot pick either. There is no
// capability gate here: settings is `kv` made frontend-reachable, and the token's
// audience + tenant already bound it. (Distinct from Require, which needs the backend
// double token; a frontend has no service token.)
func (a *Authorizer) RequireFrontend(r *http.Request) (Caller, int, error) {
	if a.signer == nil {
		return Caller{}, http.StatusServiceUnavailable, errors.New("plugin auth unavailable")
	}
	tok := r.Header.Get(pluginproto.FrontendTokenHeader)
	if tok == "" {
		return Caller{}, http.StatusUnauthorized, errors.New("frontend token required")
	}
	ac, err := a.signer.VerifyFrontendToken(r.Context(), tok, a.now())
	if err != nil {
		return Caller{}, http.StatusUnauthorized, fmt.Errorf("invalid frontend token: %w", err)
	}
	pluginID, ok := pluginproto.PluginIDFromSubject(ac.Aud)
	if !ok {
		return Caller{}, http.StatusUnauthorized, errors.New("frontend token has no plugin audience")
	}
	return Caller{PluginID: pluginID, ProjectID: ac.ProjectID, Actor: ac.Actor}, http.StatusOK, nil
}

// RequirePluginToken verifies the SERVICE TOKEN ALONE (no user assertion) and the
// capability — the auth model for PLUGIN-INITIATED operations like cold-path
// ingest (H7 finding #3). Cold-path ingest arrives from an external source
// directly at the plugin, so there is no user whose permissions to intersect; the
// plugin's service token proves which plugin it is, and the caller scopes the
// target project itself (the plugin's own project, never the request body).
func (a *Authorizer) RequirePluginToken(r *http.Request, capability string) (pluginID string, status int, err error) {
	if a.signer == nil {
		return "", http.StatusServiceUnavailable, errors.New("plugin auth unavailable")
	}
	svc := r.Header.Get("X-LLMObs-Service-Token")
	if svc == "" {
		return "", http.StatusUnauthorized, errors.New("service token required")
	}
	stc, err := a.signer.VerifyServiceToken(r.Context(), svc, a.now())
	if err != nil {
		return "", http.StatusUnauthorized, fmt.Errorf("invalid service token: %w", err)
	}
	caps, _ := perm.SplitCapsAndPerms(stc.Scopes)
	if !perm.Has(caps, perm.CapMarker(capability)) {
		return "", http.StatusForbidden, fmt.Errorf("plugin lacks capability %q", capability)
	}
	return stc.PluginID, http.StatusOK, nil
}
