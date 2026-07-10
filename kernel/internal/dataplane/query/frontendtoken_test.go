package query

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/authhttp"
)

// frontendReq crafts a browser→kernel request carrying ONLY a plugin frontend
// token (the J1 credential) — no service token, no session cookie. The token is a
// kernel-minted identity assertion whose scopes were already intersected at mint
// (plugin-grant ∩ user-session), which is exactly what frontendtoken.Handler emits.
func frontendReq(t *testing.T, signer *plugintoken.Signer, pluginID string, scopes []string, projectID string, age, ttl time.Duration) *http.Request {
	t.Helper()
	tok, _, err := signer.MintFrontendToken(pluginID, "u@x", projectID, "frontend:u@x", scopes, time.Now().Add(-age), ttl)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1alpha1/query", nil)
	r.Header.Set("X-LLMObs-Frontend-Token", tok)
	return r
}

// TestFrontendTokenLeastPrivilege proves the POSITIVE half of the J1 ruling: for a
// cooperating SDK-using frontend, the frontend token delivers least-privilege — the
// token's scopes (already plugin ∩ user ∩ project) are the ceiling, over-reach is
// denied, and the token cannot pick another tenant or survive forgery/expiry.
func TestFrontendTokenLeastPrivilege(t *testing.T) {
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	forged, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{signer: signer}
	const A, B = "projA", "projB"

	// 1) metadata-only token → metadata query ALLOWED, payloads NOT reachable.
	t.Run("metadata_stays_metadata", func(t *testing.T) {
		r := frontendReq(t, signer, "acme/a", []string{perm.TracesReadMetadata}, A, 0, time.Minute)
		id, aerr := s.auth(r, "query")
		if aerr != nil {
			t.Fatalf("metadata query must be allowed, got %v", aerr)
		}
		if id.ProjectID != A {
			t.Fatalf("project must come from the token, got %s", id.ProjectID)
		}
		if perm.Has(id.Scopes, perm.TracesReadPayloads) {
			t.Fatalf("metadata-only token must not reach payloads, got %v", id.Scopes)
		}
	})

	// 2) a token WITHOUT payloads cannot perform a payload-scoped read → DENY. The
	// token is the ceiling; the frontend cannot exceed what was minted for it.
	t.Run("cannot_exceed_token", func(t *testing.T) {
		r := frontendReq(t, signer, "acme/a", []string{perm.TracesReadMetadata}, A, 0, time.Minute)
		if _, aerr := s.auth(r, "query:payloads"); aerr == nil {
			t.Fatal("token without payloads must be denied a payload read")
		}
	})

	// 3) cross-project: the token is pinned to B; it cannot operate on A.
	t.Run("cross_project_pinned", func(t *testing.T) {
		r := frontendReq(t, signer, "acme/a", []string{perm.TracesReadMetadata}, B, 0, time.Minute)
		id, aerr := s.auth(r, "query")
		if aerr != nil {
			t.Fatalf("want allow, got %v", aerr)
		}
		if id.ProjectID != B {
			t.Fatalf("frontend token must be pinned to its project B, got %s", id.ProjectID)
		}
	})

	// 4) forged (foreign key) → DENY.
	t.Run("deny_forged", func(t *testing.T) {
		r := frontendReq(t, forged, "acme/a", []string{perm.TracesReadMetadata}, A, 0, time.Minute)
		if _, aerr := s.auth(r, "query"); aerr == nil {
			t.Fatal("frontend token signed by a foreign key must be rejected")
		}
	})

	// 5) expired → DENY.
	t.Run("deny_expired", func(t *testing.T) {
		r := frontendReq(t, signer, "acme/a", []string{perm.TracesReadMetadata}, A, 10*time.Minute, time.Minute)
		if _, aerr := s.auth(r, "query"); aerr == nil {
			t.Fatal("expired frontend token must be rejected")
		}
	})

	// 6) frontend tokens carry NO capability markers, so they can never reach a
	// capability-gated primitive path — the mint emits data perms only. (Belt: even a
	// token that somehow named a full-scope set is bounded by the reqPerm check.)
	t.Run("no_capability_escalation", func(t *testing.T) {
		// A token whose scopes accidentally include a cap marker still only satisfies
		// data-perm checks; delete (erasure) requires traces:delete which is not here.
		r := frontendReq(t, signer, "acme/a", []string{perm.TracesReadMetadata, perm.ScoresRead}, A, 0, time.Minute)
		if _, aerr := s.auth(r, "delete"); aerr == nil {
			t.Fatal("a read-scoped frontend token must not authorize erasure")
		}
	})
}

// TestProxyAssertionCannotBeReplayedAsFrontendToken is the regression for the
// token-class-confusion escalation the J1 security review caught: the gateway proxy
// (and the jobs runner) mint an identity assertion of the SAME claim shape but with
// UN-INTERSECTED scopes (the user's full role scopes) — deliberately, because the
// double-token intersection confines them later at authPlugin. A malicious BACKEND
// plugin holds such an assertion (the proxy injects it). It must NOT be able to
// replay that assertion onto the frontend seam (no service token, so no
// intersection) to obtain full scopes. The signed PurposeFrontend marker is what
// makes the two classes distinguishable; only MintFrontendToken sets it.
func TestProxyAssertionCannotBeReplayedAsFrontendToken(t *testing.T) {
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{signer: signer}

	// A proxy-style assertion: full admin scopes, aud=plugin:{id}, NO purpose marker
	// (exactly what pluginproxy.MintIdentityAssertion injects into a backend plugin).
	proxyTok, _, err := signer.MintIdentityAssertion("evil/plug", "admin@x", "projA", "session:admin@x", perm.All(), time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1alpha1/query", nil)
	r.Header.Set("X-LLMObs-Frontend-Token", proxyTok)

	// The replay must be denied — a bare user-scoped (un-intersected) assertion has no
	// PurposeFrontend marker, so VerifyFrontendToken rejects it.
	if _, aerr := s.auth(r, "query"); aerr == nil {
		t.Fatal("a proxy/jobs identity assertion (full scopes, no purpose) MUST be rejected on the frontend seam")
	}
	// And the same for the erasure path the capability gate otherwise forbids to all
	// plugins — the replay must not reach delete either.
	if _, aerr := s.auth(r, "delete"); aerr == nil {
		t.Fatal("the replayed assertion must not reach erasure via the frontend seam")
	}
}

// TestFrontendTokenIsNotABoundary is the HONEST NEGATIVE the J1 ruling demands:
// the frontend token is least-privilege-BY-DEFAULT, NOT a containment boundary. A
// plugin frontend runs in the shell's origin + JS realm (ADR-0004), so it can DROP
// the SDK's frontend token and call the Query API with the ambient session cookie
// instead — Case 2 — and get the user's FULL session scope. This test proves the
// limitation exists and is named, rather than hiding it.
//
// The real boundary for untrusted frontends is origin isolation (ADR-0004
// amendment / tracking issue), built when the first untrusted third-party frontend
// plugin is a real requirement. Until then: a frontend that needs HARD confinement
// runs a backend (H3 truly confines backends).
func TestFrontendTokenIsNotABoundary(t *testing.T) {
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{signer: signer}

	// The plugin was minted a deliberately NARROW frontend token (metadata only)...
	narrow := frontendReq(t, signer, "acme/a", []string{perm.TracesReadMetadata}, "projA", 0, time.Minute)
	if id, aerr := s.auth(narrow, "query:payloads"); aerr == nil {
		t.Fatalf("sanity: the narrow token itself must NOT reach payloads, got scopes %v", id.Scopes)
	}

	// ...but the SAME frontend, running same-origin, can simply present the user's
	// session (Case 2) and obtain the user's full role scopes — bypassing the token.
	admin := controlplane.Session{User: controlplane.User{Email: "u@x", Role: "admin"}}
	bypass := httptest.NewRequest(http.MethodPost, "/v1alpha1/query", nil)
	bypass.Header.Set("X-LLMObs-Project", "projA") // avoid the DB project lookup in unit test
	bypass = bypass.WithContext(authhttp.WithSession(bypass.Context(), admin))

	id, aerr := s.auth(bypass, "query:payloads")
	if aerr != nil {
		t.Fatalf("the same-origin session bypass is EXPECTED to succeed (this is the documented limitation): %v", aerr)
	}
	if !perm.Has(id.Scopes, perm.TracesReadPayloads) {
		t.Fatal("bypass must yield the user's FULL scopes — that is precisely why the frontend token is not a boundary")
	}
	// Documented, proven, named: confinement of a hostile frontend requires origin
	// isolation, not this token. See ADR-0004 amendment + ADR-0023 J1 section.
}
