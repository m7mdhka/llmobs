package authhttp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/secretbox"
)

// OIDC/SSO — the FIRST external trust boundary. An IdP-asserted identity is a
// CLIENT-SUPPLIED claim until fully verified; the entire security of this file is the ordering:
// signature (against the IdP JWKS) + issuer + audience + expiry (via go-oidc's verifier) AND the
// browser state (CSRF) AND the nonce (replay) are ALL checked BEFORE any claim field (email,
// groups) is read. A role/identity from an unverified assertion never grants access. NO plan
// gating — SSO ships in the OSS core.

const ssoStateCookie = "llmobs_sso"

// SSOHandler runs the OIDC flow and the per-org config endpoints. It hangs off *Handler to reuse
// the pool, the instanceAdmin/CSRF/session machinery, and setSessionCookie.
type SSOHandler struct {
	h          *Handler
	box        *secretbox.Box // seals the client secret at rest; only decrypted in memory
	httpClient *http.Client   // bounded outbound client for discovery/JWKS/token exchange
	publicURL  string         // base for redirect_uri (e.g. https://obs.example.com); "" => derive from request
	// loginOrg resolves the single org SSO authenticates INTO. In the current single-org login
	// model, a session's authority is always resolved (by ResolveSession) from the default org,
	// so SSO must provision into THAT org — otherwise a login into another org would mint a
	// session carrying the user's default-org authority (a cross-org escalation the boundary
	// review caught). Injectable for tests; defaults to controlplane.DefaultOrgID. The provider
	// config table is per-org and multi-org-ready; per-org SSO LOGIN awaits per-org session
	// authority (future work).
	loginOrg func(context.Context) (string, error)
}

// RegisterSSO mounts the SSO routes. The FLOW endpoints (providers/start/callback) are
// UNAUTHENTICATED (there is no session yet) and CSRF-exempt at the session layer — they use the
// OIDC `state` param for CSRF instead. The CONFIG endpoints are session-authed and org:manage-
// gated. box may be nil (SSO disabled); httpClient must be timeout-bounded.
func (h *Handler) RegisterSSO(mux *http.ServeMux, box *secretbox.Box, httpClient *http.Client, publicURL string) *SSOHandler {
	s := &SSOHandler{
		h: h, box: box, httpClient: httpClient, publicURL: strings.TrimRight(publicURL, "/"),
		loginOrg: func(ctx context.Context) (string, error) { return controlplane.DefaultOrgID(ctx, h.pool) },
	}
	mux.HandleFunc("/auth/sso/providers", s.providers) // GET: discovery for the login page
	mux.HandleFunc("/auth/sso/", s.flow)               // /auth/sso/{org}/start | /callback
	mux.Handle("/v1alpha1/sso/", h.RequireAuth(http.HandlerFunc(s.config)))
	return s
}

// isLoginOrg reports whether org is the single org SSO authenticates into (the login org). SSO
// on any other org would provision into an org whose role the session does NOT resolve, so it is
// refused. Fails closed on a resolution error.
func (s *SSOHandler) isLoginOrg(ctx context.Context, org string) bool {
	lo, err := s.loginOrg(ctx)
	return err == nil && lo != "" && lo == org
}

// config handles GET/PUT /v1alpha1/sso/{org}. Gated on org:manage in the TARGET org (configuring
// external identity is an owner-level, per-org action — resolved against the org it targets).
// PUT caps every group→role entry strictly-below the configurer's own role (the escalation cap
// on this JIT-provisioning path — so SSO can never be configured to grant owner) and refuses
// to disable local passwords if it would leave the org with no local-password owner (break-glass).
func (s *SSOHandler) config(w http.ResponseWriter, r *http.Request) {
	if s.box == nil {
		writeErr(w, http.StatusNotFound, "not_found", "SSO is not enabled on this instance")
		return
	}
	org := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1alpha1/sso/"), "/")
	if org == "" || strings.Contains(org, "/") {
		writeErr(w, http.StatusNotFound, "not_found", "unknown SSO resource")
		return
	}
	sess, _ := SessionFrom(r.Context())
	configurerRole, err := controlplane.RoleInOrg(r.Context(), s.h.pool, sess.User.ID, org)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "authority resolution failed")
		return
	}
	if !perm.Has(perm.RoleScopes(configurerRole), perm.OrgManage) {
		writeErr(w, http.StatusForbidden, "forbidden", "configuring SSO requires org:manage (owner) in this org")
		return
	}
	// Single-login-org SSO: SSO authenticates only into the login org, so configuring
	// it for any other org would be dead config that could never mint a coherent session.
	if !s.isLoginOrg(r.Context(), org) {
		writeErr(w, http.StatusConflict, "sso_not_login_org", "SSO login is only supported for the instance's login org in this version")
		return
	}

	switch r.Method {
	case http.MethodGet:
		view, err := controlplane.GetSSOProviderView(r.Context(), s.h.pool, org)
		if err == controlplane.ErrNoSSOProvider {
			writeErr(w, http.StatusNotFound, "not_found", "no SSO provider configured")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "read failed")
			return
		}
		writeJSON(w, http.StatusOK, view)
	case http.MethodPut:
		var req struct {
			Issuer                string            `json:"issuer"`
			ClientID              string            `json:"client_id"`
			ClientSecret          string            `json:"client_secret"`
			GroupClaim            string            `json:"group_claim"`
			LocalPasswordDisabled bool              `json:"local_password_disabled"`
			Enabled               *bool             `json:"enabled"`
			GroupRoles            map[string]string `json:"group_roles"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "schema_invalid", "invalid JSON")
			return
		}
		// The crown cap: no group may be mapped to a role at or above the configurer's own.
		for g, role := range req.GroupRoles {
			if !perm.ValidRole(role) {
				writeErr(w, http.StatusBadRequest, "schema_invalid", "group '"+g+"' maps to an invalid role")
				return
			}
			if !perm.RoleAbove(configurerRole, role) {
				writeErr(w, http.StatusForbidden, "forbidden", "cannot map group '"+g+"' to a role at or above your own")
				return
			}
		}
		// Break-glass: disabling local passwords must not leave the org unable to log in without
		// the IdP — require at least one owner who still has a local password.
		if req.LocalPasswordDisabled {
			n, err := controlplane.CountLocalOwners(r.Context(), s.h.pool, org)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "internal", "owner check failed")
				return
			}
			if n < 1 {
				writeErr(w, http.StatusConflict, "would_lock_out", "cannot disable local passwords: no owner with a local password remains (break-glass)")
				return
			}
		}
		// Preserve the existing secret if the caller sent none (so editing config doesn't force
		// re-entering it); require one when first configuring.
		secret := req.ClientSecret
		if secret == "" {
			existing, gerr := controlplane.GetSSOProvider(r.Context(), s.h.pool, s.box, org)
			if gerr == nil {
				secret = existing.ClientSecret
			} else {
				writeErr(w, http.StatusBadRequest, "schema_invalid", "client_secret is required to configure SSO")
				return
			}
		}
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		p := &controlplane.SSOProvider{
			OrgID: org, Issuer: strings.TrimSpace(req.Issuer), ClientID: strings.TrimSpace(req.ClientID),
			ClientSecret: secret, GroupClaim: req.GroupClaim, LocalPasswordDisabled: req.LocalPasswordDisabled,
			Enabled: enabled, GroupRoles: req.GroupRoles,
		}
		if err := controlplane.SetSSOProvider(r.Context(), s.h.pool, s.box, p); err != nil {
			writeErr(w, http.StatusBadRequest, "schema_invalid", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or PUT")
	}
}

// providers lists orgs offering SSO (unauthenticated) so the login page can render the button.
func (s *SSOHandler) providers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	if s.box == nil {
		writeJSON(w, http.StatusOK, map[string]any{"providers": []any{}})
		return
	}
	all, err := controlplane.ListEnabledSSOOrgs(r.Context(), s.h.pool)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	// Only advertise the login org's provider — the only one the flow will honor.
	orgs := []controlplane.EnabledSSOOrg{}
	for _, o := range all {
		if s.isLoginOrg(r.Context(), o.OrgID) {
			orgs = append(orgs, o)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": orgs})
}

// flow routes /auth/sso/{org}/{start|callback}.
func (s *SSOHandler) flow(w http.ResponseWriter, r *http.Request) {
	if s.box == nil {
		writeErr(w, http.StatusNotFound, "not_found", "SSO is not enabled on this instance")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/auth/sso/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" {
		writeErr(w, http.StatusNotFound, "not_found", "unknown SSO route")
		return
	}
	org, action := parts[0], parts[1]
	// SSO authenticates ONLY into the login org (see isLoginOrg): provisioning into any other
	// org would mint a session whose authority ResolveSession resolves elsewhere — a cross-org
	// escalation. Refuse before touching the IdP.
	if !s.isLoginOrg(r.Context(), org) {
		writeErr(w, http.StatusNotFound, "not_found", "SSO login is not enabled for this org")
		return
	}
	switch action {
	case "start":
		s.start(w, r, org)
	case "callback":
		s.callback(w, r, org)
	default:
		writeErr(w, http.StatusNotFound, "not_found", "unknown SSO action")
	}
}

// ssoState is the CSRF+replay material stashed in an HttpOnly cookie between start and callback.
type ssoState struct {
	State string `json:"s"`
	Nonce string `json:"n"`
	Org   string `json:"o"`
}

func randB64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// baseURL is the external origin used to build the redirect_uri. It MUST be identical between
// start and callback and must match what is registered at the IdP. A configured publicURL wins
// over the (spoofable) Host header.
func (s *SSOHandler) baseURL(r *http.Request) string {
	if s.publicURL != "" {
		return s.publicURL
	}
	scheme := "http"
	if s.h.secure {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *SSOHandler) oidcProvider(ctx context.Context, issuer string) (*oidc.Provider, error) {
	return oidc.NewProvider(oidc.ClientContext(ctx, s.httpClient), issuer)
}

func (s *SSOHandler) oauth2Config(p *controlplane.SSOProvider, provider *oidc.Provider, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  redirectURL,
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}
}

// start begins the auth-code flow: discover the IdP, stash state+nonce in an HttpOnly cookie,
// and 302 to the IdP authorize endpoint. No identity is trusted here — this only kicks off.
func (s *SSOHandler) start(w http.ResponseWriter, r *http.Request, org string) {
	p, err := controlplane.GetSSOProvider(r.Context(), s.h.pool, s.box, org)
	if err != nil || !p.Enabled {
		writeErr(w, http.StatusNotFound, "not_found", "no SSO provider for this org")
		return
	}
	provider, err := s.oidcProvider(r.Context(), p.Issuer)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "idp_unreachable", "OIDC discovery failed")
		return
	}
	state, err1 := randB64(32)
	nonce, err2 := randB64(32)
	if err1 != nil || err2 != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "state generation failed")
		return
	}
	blob, _ := json.Marshal(ssoState{State: state, Nonce: nonce, Org: org})
	http.SetCookie(w, &http.Cookie{
		Name:     ssoStateCookie,
		Value:    base64.RawURLEncoding.EncodeToString(blob),
		Path:     "/auth/sso/",
		HttpOnly: true,
		Secure:   s.h.secure,
		SameSite: http.SameSiteLaxMode, // Lax: the IdP's top-level GET redirect carries it back
		MaxAge:   600,                  // 10 minutes to complete the round-trip
	})
	cfg := s.oauth2Config(p, provider, s.baseURL(r)+"/auth/sso/"+org+"/callback")
	http.Redirect(w, r, cfg.AuthCodeURL(state, oidc.Nonce(nonce)), http.StatusFound)
}

// callback completes the flow. THE ORDER IS THE SECURITY: state (CSRF) → code exchange → ID
// token verify (sig/iss/aud/exp) → nonce (replay) → ONLY THEN read email/groups → group→role
// (fail closed) → revocation → JIT → session.
func (s *SSOHandler) callback(w http.ResponseWriter, r *http.Request, org string) {
	// 1. Recover and immediately clear the state cookie.
	c, err := r.Cookie(ssoStateCookie)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "sso_state_missing", "missing SSO state; restart the login")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: ssoStateCookie, Path: "/auth/sso/", MaxAge: -1, HttpOnly: true, Secure: s.h.secure})
	raw, derr := base64.RawURLEncoding.DecodeString(c.Value)
	var st ssoState
	if derr != nil || json.Unmarshal(raw, &st) != nil {
		writeErr(w, http.StatusBadRequest, "sso_state_invalid", "invalid SSO state")
		return
	}
	// 2. The cookie's org must match the path org, and the query state must match the cookie
	//    state (CSRF: an attacker cannot forge a request that carries our HttpOnly cookie value).
	if st.Org != org || subtle.ConstantTimeCompare([]byte(st.State), []byte(r.URL.Query().Get("state"))) != 1 {
		writeErr(w, http.StatusForbidden, "sso_state_mismatch", "SSO state mismatch (possible CSRF)")
		return
	}
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		writeErr(w, http.StatusUnauthorized, "idp_error", "the identity provider rejected the login")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeErr(w, http.StatusBadRequest, "sso_no_code", "no authorization code")
		return
	}

	p, err := controlplane.GetSSOProvider(r.Context(), s.h.pool, s.box, org)
	if err != nil || !p.Enabled {
		writeErr(w, http.StatusNotFound, "not_found", "no SSO provider for this org")
		return
	}
	provider, err := s.oidcProvider(r.Context(), p.Issuer)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "idp_unreachable", "OIDC discovery failed")
		return
	}
	// 3. Exchange the code for tokens (server-to-server, authenticated with the client secret).
	cfg := s.oauth2Config(p, provider, s.baseURL(r)+"/auth/sso/"+org+"/callback")
	oauthTok, err := cfg.Exchange(oidc.ClientContext(r.Context(), s.httpClient), code)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "sso_exchange_failed", "code exchange failed")
		return
	}
	rawID, ok := oauthTok.Extra("id_token").(string)
	if !ok || rawID == "" {
		writeErr(w, http.StatusUnauthorized, "sso_no_id_token", "no ID token returned")
		return
	}
	// 4. VERIFY the ID token: signature against the IdP JWKS, issuer == provider issuer,
	//    audience == our client_id, and expiry. A forged / expired / wrong-audience /
	//    wrong-issuer token fails HERE, before any claim is read.
	idToken, err := provider.Verifier(&oidc.Config{ClientID: p.ClientID}).Verify(r.Context(), rawID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "sso_token_invalid", "ID token verification failed")
		return
	}
	// 5. Nonce (replay): the verified token's nonce must equal the one we minted at start.
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(st.Nonce)) != 1 {
		writeErr(w, http.StatusForbidden, "sso_nonce_mismatch", "ID token nonce mismatch (possible replay)")
		return
	}
	// 6. ONLY NOW is it safe to read claims — the assertion is fully verified.
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		writeErr(w, http.StatusUnauthorized, "sso_claims_invalid", "could not read ID token claims")
		return
	}
	email := strings.TrimSpace(stringClaim(claims, "email"))
	if email == "" {
		writeErr(w, http.StatusUnauthorized, "sso_no_email", "the IdP did not assert an email")
		return
	}
	// If the IdP asserts email_verified, it MUST be affirmatively true. Coerce robustly and
	// FAIL CLOSED: some IdPs emit the claim as the STRING "false" (older Azure AD v1, some
	// Keycloak configs) — a naive bool assertion would miss it and trust an explicitly-
	// unverified email (an account-linking takeover vector). An ABSENT claim is accepted: an
	// enterprise IdP's directory is authoritative for its emails and often omits it (documented
	// trust assumption). Any present-but-not-true value is rejected.
	if !emailVerifiedOK(claims) {
		writeErr(w, http.StatusUnauthorized, "sso_email_unverified", "the IdP reports this email as unverified")
		return
	}
	groups := stringsClaim(claims, p.GroupClaim)

	// 7. Role comes ONLY from the verified group→role map. No mapped group → NO access (not a
	//    fallback role). This is the fail-closed heart of SSO authorization.
	role := controlplane.SSORoleForGroups(p.GroupRoles, groups)
	if role == "" {
		writeErr(w, http.StatusForbidden, "sso_no_role", "your account is not in a group mapped to a role")
		return
	}
	// 8. A revoked user cannot re-enter via SSO either.
	if revoked, rerr := controlplane.UserRevoked(r.Context(), s.h.pool, email); rerr != nil || revoked {
		writeErr(w, http.StatusForbidden, "revoked", "this account has been revoked")
		return
	}
	// 9. JIT-provision through the same provisioning discipline: into THIS org only, at the
	//    capped mapped role, never granting or downgrading an owner.
	uid, err := controlplane.JITProvisionSSOUser(r.Context(), s.h.pool, org, email, role)
	if err == controlplane.ErrLocalAccountExists {
		writeErr(w, http.StatusForbidden, "sso_local_account", "an account with this email uses local login; sign in with your password")
		return
	}
	if err != nil {
		s.h.log.Error("sso jit provision failed", "err", err.Error())
		writeErr(w, http.StatusInternalServerError, "internal", "provisioning failed")
		return
	}
	// 10. Mint the SAME kind of session a local login would — role is re-resolved server-side.
	sess, err := controlplane.CreateSession(r.Context(), s.h.pool, controlplane.User{ID: uid, Email: email})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "session creation failed")
		return
	}
	s.h.setSessionCookie(w, sess.Token, sess.ExpiresAt)
	http.Redirect(w, r, "/", http.StatusFound)
}

// emailVerifiedOK reports whether the ID token's email may be trusted as an identity. It FAILS
// CLOSED on a present-but-not-affirmatively-true claim (a bool false, the string "false", or any
// non-true value — some IdPs emit email_verified as a STRING, which a naive bool assertion would
// miss and wrongly trust). An ABSENT claim is accepted: an enterprise IdP's directory is
// authoritative for its emails and often omits it (a deliberate, documented trust assumption).
func emailVerifiedOK(claims map[string]any) bool {
	v, present := claims["email_verified"]
	if !present {
		return true
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true")
	default:
		return false
	}
}

// stringClaim reads a string claim (email etc.).
func stringClaim(claims map[string]any, key string) string {
	if v, ok := claims[key].(string); ok {
		return v
	}
	return ""
}

// stringsClaim reads a claim that is an array of strings (groups), tolerating a single string.
func stringsClaim(claims map[string]any, key string) []string {
	switch v := claims[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		return []string{v}
	}
	return nil
}
