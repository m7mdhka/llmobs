package query

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// buildReq crafts a plugin→kernel request carrying a service token (plugin scopes)
// and an identity assertion (user scopes, project, audience), both minted by the
// given signers.
func buildReq(t *testing.T, svcSigner, asrSigner *plugintoken.Signer, tokPluginID, asrPluginID string, pluginScopes, userScopes []string, projectID string, asrAge, asrTTL time.Duration) *http.Request {
	t.Helper()
	now := time.Now()
	svc, _, err := svcSigner.MintServiceToken(tokPluginID, pluginScopes, now, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	asr, _, err := asrSigner.MintIdentityAssertion(asrPluginID, "user-1", projectID, "session:u@x", userScopes, now.Add(-asrAge), asrTTL)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1alpha1/query", nil)
	r.Header.Set("X-LLMObs-Service-Token", svc)
	r.Header.Set(pluginproto.IdentityAssertionHeader, asr)
	return r
}

// TestIntersectionMatrix is the security proof the whole platform rests on: for a
// plugin acting on behalf of a user, effective access is the intersection of the
// plugin's grant, the user's grant, and the project — and every over-reach is
// denied.
func TestIntersectionMatrix(t *testing.T) {
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	other, err := plugintoken.NewSigner() // a different (forged) key
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{signer: signer}

	capQuery := perm.CapMarker("query")
	capIngest := perm.CapMarker("ingest")
	const A, B = "projA", "projB"

	// 1) metadata plugin + full user, op=query -> ALLOW, metadata only, project A.
	t.Run("allow_metadata", func(t *testing.T) {
		r := buildReq(t, signer, signer, "acme/a", "acme/a",
			[]string{capQuery, perm.TracesReadMetadata}, perm.All(), A, 0, time.Minute)
		id, aerr := s.auth(r, "query")
		if aerr != nil {
			t.Fatalf("want allow, got %v", aerr)
		}
		if id.ProjectID != A {
			t.Fatalf("project must come from the assertion, got %s", id.ProjectID)
		}
		if !perm.Has(id.Scopes, perm.TracesReadMetadata) || perm.Has(id.Scopes, perm.TracesReadPayloads) {
			t.Fatalf("effective must be metadata-only, got %v", id.Scopes)
		}
	})

	// 2) plugin has payloads but the USER does not -> payloads blocked (user half caps).
	t.Run("user_half_caps_payloads", func(t *testing.T) {
		r := buildReq(t, signer, signer, "acme/a", "acme/a",
			[]string{capQuery, perm.TracesReadMetadata, perm.TracesReadPayloads},
			[]string{perm.TracesReadMetadata}, A, 0, time.Minute)
		id, aerr := s.auth(r, "query")
		if aerr != nil {
			t.Fatalf("want allow (metadata), got %v", aerr)
		}
		if perm.Has(id.Scopes, perm.TracesReadPayloads) {
			t.Fatal("user without payloads must cap the plugin's payload access")
		}
	})

	// 3) plugin has metadata only but the USER has payloads -> plugin half caps.
	t.Run("plugin_half_caps_payloads", func(t *testing.T) {
		r := buildReq(t, signer, signer, "acme/a", "acme/a",
			[]string{capQuery, perm.TracesReadMetadata}, perm.All(), A, 0, time.Minute)
		id, _ := s.auth(r, "query")
		if perm.Has(id.Scopes, perm.TracesReadPayloads) {
			t.Fatal("metadata-only plugin must not reach payloads even for a full user")
		}
	})

	// 4) cross-project: the assertion is scoped to B; the plugin cannot reach A.
	t.Run("cross_project_isolated", func(t *testing.T) {
		r := buildReq(t, signer, signer, "acme/a", "acme/a",
			[]string{capQuery, perm.TracesReadMetadata}, perm.All(), B, 0, time.Minute)
		id, aerr := s.auth(r, "query")
		if aerr != nil {
			t.Fatalf("want allow, got %v", aerr)
		}
		if id.ProjectID != B {
			t.Fatalf("plugin must be pinned to the assertion's project B, got %s", id.ProjectID)
		}
	})

	// 5) plugin lacks the capability for the op -> DENY.
	t.Run("deny_missing_capability", func(t *testing.T) {
		r := buildReq(t, signer, signer, "acme/a", "acme/a",
			[]string{capIngest, perm.TracesReadMetadata}, perm.All(), A, 0, time.Minute)
		if _, aerr := s.auth(r, "query"); aerr == nil {
			t.Fatal("plugin without cap:query must be denied a query")
		}
	})

	// 6) audience confusion: assertion minted for plugin B, token for plugin A -> DENY.
	t.Run("deny_audience_confusion", func(t *testing.T) {
		r := buildReq(t, signer, signer, "acme/a", "acme/b",
			[]string{capQuery, perm.TracesReadMetadata}, perm.All(), A, 0, time.Minute)
		if _, aerr := s.auth(r, "query"); aerr == nil {
			t.Fatal("assertion minted for another plugin must be rejected")
		}
	})

	// 7) expired assertion -> DENY.
	t.Run("deny_expired_assertion", func(t *testing.T) {
		r := buildReq(t, signer, signer, "acme/a", "acme/a",
			[]string{capQuery, perm.TracesReadMetadata}, perm.All(), A, 10*time.Minute, time.Minute)
		if _, aerr := s.auth(r, "query"); aerr == nil {
			t.Fatal("expired assertion must be rejected")
		}
	})

	// 8) forged service token (wrong key) -> DENY.
	t.Run("deny_forged_service_token", func(t *testing.T) {
		r := buildReq(t, other, signer, "acme/a", "acme/a",
			[]string{capQuery, perm.TracesReadMetadata}, perm.All(), A, 0, time.Minute)
		if _, aerr := s.auth(r, "query"); aerr == nil {
			t.Fatal("service token signed by a foreign key must be rejected")
		}
	})

	// 9) plugins may not perform GDPR erasure (delete has no capability) -> DENY.
	t.Run("deny_plugin_erasure", func(t *testing.T) {
		r := buildReq(t, signer, signer, "acme/a", "acme/a",
			[]string{capQuery, perm.TracesReadMetadata, perm.TracesDelete}, perm.All(), A, 0, time.Minute)
		if _, aerr := s.auth(r, "delete"); aerr == nil {
			t.Fatal("no plugin capability grants erasure")
		}
	})
}
