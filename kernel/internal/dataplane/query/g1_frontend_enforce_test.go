package query

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/authhttp"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// adminSession returns a context-injected full-admin session (RoleScopes("admin") ==
// the FULL permission set) — the ambient browser session every plugin frontend runs
// under. The whole point of frontend-token enforcement is that a plugin must NOT inherit this.
func adminSessionReq(method string) *http.Request {
	r := httptest.NewRequest(method, "/v1alpha1/query", nil)
	sess := controlplane.Session{User: controlplane.User{ID: "u1", Email: "user@x", Role: "admin"}}
	return r.WithContext(authhttp.WithSession(r.Context(), sess))
}

// TestG1FrontendEnforcedAgainstAmbientSession is the prove-the-negative: the live
// privilege-escalation the audit found. A frontend-only plugin, calling the Query API
// from the browser with a VALID admin user session, must be confined to its minted
// (intersected) grant — never the user's full session scope — and a marked plugin
// request whose token is missing/expired must FAIL CLOSED, not silently escalate.
func TestG1FrontendEnforcedAgainstAmbientSession(t *testing.T) {
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{signer: signer}
	// The shell's own session (Case 2) resolves its per-project-org role (admin → full);
	// injected (no DB in this unit test). Enforcement confines PLUGINS, not the first-party shell.
	s.SetRoleResolver(func(_ context.Context, _, _ string) (string, error) { return "admin", nil })
	const grantedProject, otherProject = "projA", "projB"

	// The frontend token the shell mints for a METADATA-ONLY plugin: even though the
	// SESSION is admin (full scope), the mint intersected plugin-grant ∩ session, so the
	// token carries metadata-read only. (frontendtoken.Handler computes this; here we
	// mint the already-intersected result directly.)
	metadataToken := func(project string) string {
		tok, _, e := signer.MintFrontendToken("acme/metadata-plugin", "user@x", project,
			"frontend:user@x", []string{perm.TracesReadMetadata}, time.Now(), time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		return tok
	}

	// (a) A metadata plugin with its token AND the ambient admin session must NOT read
	// payloads — the token is the ceiling, not the session.
	t.Run("a_cannot_read_payloads_despite_admin_session", func(t *testing.T) {
		r := adminSessionReq(http.MethodPost)
		r.Header.Set(pluginproto.PluginFrontendHeader, "1")
		r.Header.Set(pluginproto.FrontendTokenHeader, metadataToken(grantedProject))
		id, aerr := s.auth(r, "query")
		if aerr != nil {
			t.Fatalf("metadata query must be allowed, got %v", aerr)
		}
		if perm.Has(id.Scopes, perm.TracesReadPayloads) {
			t.Fatal("a metadata frontend plugin must NOT reach payloads even under an admin session")
		}
		// The payload-scoped op is denied outright.
		if _, aerr := s.auth(reAuth(r), "query:payloads"); aerr == nil {
			t.Fatal("a payload read must be denied for a metadata-only frontend token")
		}
	})

	// (b) The plugin is capped at its manifest grant EVEN THOUGH the grant under-declares
	// vs the user's actual (admin/full) scope. The token = plugin-grant ∩ session; the
	// session being broader can never widen the plugin.
	t.Run("b_capped_at_grant_even_when_under_declared_vs_user", func(t *testing.T) {
		r := adminSessionReq(http.MethodPost)
		r.Header.Set(pluginproto.PluginFrontendHeader, "1")
		r.Header.Set(pluginproto.FrontendTokenHeader, metadataToken(grantedProject))
		id, aerr := s.auth(r, "query")
		if aerr != nil {
			t.Fatalf("want allow, got %v", aerr)
		}
		// The admin session would grant perm.All(); the effective set must be ONLY the
		// token's metadata scope, not the session's full set.
		if len(id.Scopes) != 1 || id.Scopes[0] != perm.TracesReadMetadata {
			t.Fatalf("effective scope must equal the plugin grant, not the admin session; got %v", id.Scopes)
		}
		// Erasure (a full-scope admin power) must be denied.
		if _, aerr := s.auth(reAuth(r), "delete"); aerr == nil {
			t.Fatal("a metadata frontend token must not authorize erasure even under an admin session")
		}
	})

	// (c) Cross-project: the token is pinned to projA; a request that also carries a
	// session and asks for projB (via header) resolves to the TOKEN's project, so the
	// plugin cannot reach another tenant.
	t.Run("c_cannot_reach_another_project", func(t *testing.T) {
		r := adminSessionReq(http.MethodPost)
		r.Header.Set(pluginproto.PluginFrontendHeader, "1")
		r.Header.Set(pluginproto.FrontendTokenHeader, metadataToken(grantedProject))
		r.Header.Set("X-LLMObs-Project", otherProject) // attempt to redirect
		id, aerr := s.auth(r, "query")
		if aerr != nil {
			t.Fatalf("want allow, got %v", aerr)
		}
		if id.ProjectID != grantedProject {
			t.Fatalf("frontend plugin must be pinned to the token's project %s, got %s (header must not redirect)", grantedProject, id.ProjectID)
		}
	})

	// (d) THE core regression: a MARKED plugin request with the ambient admin session
	// but NO frontend token (mint failed / dropped) must FAIL CLOSED — 401, NOT the
	// user's full session scope. This is the fail-open hole the audit found.
	t.Run("d_marked_without_token_fails_closed_not_full_scope", func(t *testing.T) {
		r := adminSessionReq(http.MethodPost)
		r.Header.Set(pluginproto.PluginFrontendHeader, "1") // marked plugin request, no token
		if _, aerr := s.auth(r, "query"); aerr == nil {
			t.Fatal("a marked plugin request with no token must be REJECTED, never granted the session's full scope")
		}
	})

	// (e) An EXPIRED token on a marked request fails closed too (not a silent fallback).
	t.Run("e_expired_token_fails_closed", func(t *testing.T) {
		expired, _, e := signer.MintFrontendToken("acme/metadata-plugin", "user@x", grantedProject,
			"frontend:user@x", []string{perm.TracesReadMetadata}, time.Now().Add(-10*time.Minute), time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		r := adminSessionReq(http.MethodPost)
		r.Header.Set(pluginproto.PluginFrontendHeader, "1")
		r.Header.Set(pluginproto.FrontendTokenHeader, expired)
		if _, aerr := s.auth(r, "query"); aerr == nil {
			t.Fatal("an expired frontend token on a marked request must fail closed, not fall back to session scope")
		}
	})

	// (f) CAPABILITY GATE: even a frontend token that legitimately carries traces:delete
	// (a plugin whose manifest declared it, minted under an admin session) must NOT be
	// able to perform GDPR erasure — erasure has no plugin capability and is forbidden to
	// ALL plugins, so the frontend credential must never exceed the backend one.
	t.Run("f_erasure_denied_even_with_delete_grant", func(t *testing.T) {
		delTok, _, e := signer.MintFrontendToken("acme/greedy-plugin", "user@x", grantedProject,
			"frontend:user@x", []string{perm.TracesReadMetadata, perm.TracesDelete}, time.Now(), time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		r := adminSessionReq(http.MethodDelete)
		r.Header.Set(pluginproto.PluginFrontendHeader, "1")
		r.Header.Set(pluginproto.FrontendTokenHeader, delTok)
		if _, aerr := s.auth(r, "delete"); aerr == nil {
			t.Fatal("a frontend token carrying traces:delete must STILL be denied erasure (plugins may not erase)")
		}
		// The same token can still do what it IS allowed (a metadata read).
		if _, aerr := s.auth(reAuth(r), "query"); aerr != nil {
			t.Fatalf("the token's legitimate metadata read must still work: %v", aerr)
		}
	})

	// Control: the SHELL's own request (no plugin marker, no token, admin session) still
	// gets full scope — enforcement confines plugins, it does not break the first-party shell.
	t.Run("shell_own_request_unaffected", func(t *testing.T) {
		r := adminSessionReq(http.MethodPost)
		r.Header.Set("X-LLMObs-Project", grantedProject) // shell selects the project (no pool in the test)
		id, aerr := s.auth(r, "query")
		if aerr != nil {
			t.Fatalf("shell session query must be allowed, got %v", aerr)
		}
		if !perm.Has(id.Scopes, perm.TracesReadPayloads) {
			t.Fatal("the shell's own admin session must retain full scope (not a plugin request)")
		}
	})
}

// reAuth clones a request's headers+context for a second auth() call with a different op
// (auth does not consume the body).
func reAuth(r *http.Request) *http.Request {
	c := r.Clone(r.Context())
	return c
}
