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
	stc, err := a.signer.VerifyServiceToken(svc, now)
	if err != nil {
		return Caller{}, http.StatusUnauthorized, fmt.Errorf("invalid service token: %w", err)
	}
	// The assertion MUST be bound to THIS plugin's audience (token-confusion defence).
	ac, err := a.signer.VerifyIdentityAssertion(asr, pluginproto.PluginSubject(stc.PluginID), now)
	if err != nil {
		return Caller{}, http.StatusUnauthorized, fmt.Errorf("invalid identity assertion: %w", err)
	}
	caps, _ := perm.SplitCapsAndPerms(stc.Scopes)
	if !perm.Has(caps, perm.CapMarker(capability)) {
		return Caller{}, http.StatusForbidden, fmt.Errorf("plugin lacks capability %q", capability)
	}
	return Caller{PluginID: stc.PluginID, ProjectID: ac.ProjectID, Actor: ac.Actor}, http.StatusOK, nil
}
