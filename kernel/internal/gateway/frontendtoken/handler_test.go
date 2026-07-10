package frontendtoken

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/authhttp"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/registry"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

type fakeSource struct{ plugins []registry.Plugin }

func (f fakeSource) Plugins() []registry.Plugin { return f.plugins }

func newHandler(t *testing.T, plugins []registry.Plugin) (*Handler, *plugintoken.Signer) {
	t.Helper()
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	proj := func(*http.Request) (string, error) { return "projA", nil }
	return New(signer, fakeSource{plugins: plugins}, proj), signer
}

func mint(t *testing.T, h *Handler, sess *controlplane.Session, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1alpha1/plugin-frontend-token", strings.NewReader(body))
	if sess != nil {
		r = r.WithContext(authhttp.WithSession(r.Context(), *sess))
	}
	w := httptest.NewRecorder()
	h.Mint(w, r)
	return w
}

// TestMintIntersectsGrantAndSession proves the mint computes plugin-grant ∩
// user-session: an admin gets plugin∩All (the plugin's full grant), while a viewer
// is capped to the viewer's narrower scopes — the token can never exceed EITHER
// half. The emitted token is kernel-signed and audience-bound to the plugin.
func TestMintIntersectsGrantAndSession(t *testing.T) {
	plugins := []registry.Plugin{{
		ID:          "acme/dash",
		Permissions: []string{perm.TracesReadMetadata, perm.TracesReadPayloads, perm.ScoresRead},
	}}
	h, signer := newHandler(t, plugins)

	// Admin: effective = plugin ∩ All = the plugin's full grant.
	t.Run("admin_gets_full_grant", func(t *testing.T) {
		sess := &controlplane.Session{User: controlplane.User{Email: "a@x", Role: "admin"}}
		w := mint(t, h, sess, `{"plugin":"acme/dash"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Token  string   `json:"token"`
			Scopes []string `json:"scopes"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if !perm.Has(resp.Scopes, perm.TracesReadPayloads) {
			t.Fatalf("admin should reach the plugin's full grant incl payloads, got %v", resp.Scopes)
		}
		// The token verifies as a FRONTEND token (carries the signed purpose marker);
		// the generic identity-assertion verify must REJECT it (class separation).
		ac, err := signer.VerifyFrontendToken(resp.Token, time.Now())
		if err != nil {
			t.Fatalf("token must verify as a frontend token: %v", err)
		}
		if ac.Purpose != pluginproto.PurposeFrontend {
			t.Fatalf("frontend token must carry the purpose marker, got %q", ac.Purpose)
		}
		if ac.ProjectID != "projA" {
			t.Fatalf("token project must be the session project, got %s", ac.ProjectID)
		}
		if _, err := signer.VerifyIdentityAssertion(resp.Token, pluginproto.PluginSubject("acme/dash"), time.Now()); err == nil {
			t.Fatal("a frontend token must NOT verify on the generic identity-assertion path")
		}
	})

	// Viewer: effective = plugin ∩ {metadata, scores:read} — payloads dropped by the
	// USER half even though the plugin's grant includes them.
	t.Run("viewer_capped_by_session", func(t *testing.T) {
		sess := &controlplane.Session{User: controlplane.User{Email: "v@x", Role: "viewer"}}
		w := mint(t, h, sess, `{"plugin":"acme/dash"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", w.Code)
		}
		var resp struct {
			Scopes []string `json:"scopes"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if perm.Has(resp.Scopes, perm.TracesReadPayloads) {
			t.Fatalf("a viewer session must cap payloads out of the token, got %v", resp.Scopes)
		}
		if !perm.Has(resp.Scopes, perm.TracesReadMetadata) {
			t.Fatalf("viewer keeps the metadata both halves grant, got %v", resp.Scopes)
		}
	})
}

func TestMintRejections(t *testing.T) {
	h, _ := newHandler(t, []registry.Plugin{{ID: "acme/dash", Permissions: []string{perm.TracesReadMetadata}}})
	admin := &controlplane.Session{User: controlplane.User{Email: "a@x", Role: "admin"}}

	t.Run("unauthenticated", func(t *testing.T) {
		if w := mint(t, h, nil, `{"plugin":"acme/dash"}`); w.Code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", w.Code)
		}
	})
	t.Run("unknown_plugin", func(t *testing.T) {
		if w := mint(t, h, admin, `{"plugin":"acme/nope"}`); w.Code != http.StatusNotFound {
			t.Fatalf("want 404, got %d", w.Code)
		}
	})
	t.Run("missing_plugin_field", func(t *testing.T) {
		if w := mint(t, h, admin, `{}`); w.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", w.Code)
		}
	})
	t.Run("wrong_method", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/v1alpha1/plugin-frontend-token", nil)
		r = r.WithContext(authhttp.WithSession(r.Context(), *admin))
		w := httptest.NewRecorder()
		h.Mint(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("want 405, got %d", w.Code)
		}
	})
}
