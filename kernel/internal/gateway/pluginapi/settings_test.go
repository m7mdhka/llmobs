package pluginapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginauth"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/secretbox"
	"github.com/m7mdhka/llmobs/kernel/internal/pluginsettings"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

const settingsSchemaJSON = `{
  "type": "object",
  "required": ["endpoint", "apiKey"],
  "properties": {
    "endpoint": {"type": "string", "minLength": 1},
    "enabled": {"type": "boolean"},
    "apiKey": {"type": "string", "writeOnly": true}
  }
}`

// customSecretSchemaJSON is a CUSTOM-mode plugin's schema: it declares only its
// secret (writeOnly) field. Everything else the plugin's own view stores is opaque JSON
// the kernel never subset-validates — but the secret is still encrypted + never returned.
const customSecretSchemaJSON = `{
  "type": "object",
  "properties": {
    "apiKey": {"type": "string", "writeOnly": true}
  }
}`

func settingsSetup(t *testing.T) (*Settings, *plugintoken.Signer) {
	return settingsSetupAuthz(t, func(*http.Request, string) bool { return true })
}

func settingsSetupAuthz(t *testing.T, canWrite func(*http.Request, string) bool) (*Settings, *plugintoken.Signer) {
	t.Helper()
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.NewRandom()
	if err != nil {
		t.Fatal(err)
	}
	store := pluginsettings.NewStore(newFakeKV(), box)
	schema := func(pluginID string) (json.RawMessage, bool, bool) {
		if pluginID == "acme/dash" {
			return json.RawMessage(settingsSchemaJSON), false, true
		}
		if pluginID == "acme/custom" {
			return json.RawMessage(customSecretSchemaJSON), true, true
		}
		return nil, false, false
	}
	return NewSettings(pluginauth.New(signer, nil), store, schema, canWrite), signer
}

func frontendTok(t *testing.T, signer *plugintoken.Signer, pluginID, projectID string) string {
	t.Helper()
	// The mint pre-intersects scopes; settings needs no data scope (kv-backed), so an
	// empty scope set is fine — auth here is by audience + purpose, not scope.
	tok, _, err := signer.MintFrontendToken(pluginID, "u@x", projectID, "frontend:u@x", nil, time.Now(), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func callSettings(h *Settings, op, frontendToken, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1alpha1/plugin/settings/"+op, strings.NewReader(body))
	if frontendToken != "" {
		r.Header.Set(pluginproto.FrontendTokenHeader, frontendToken)
	}
	mux := http.NewServeMux()
	h.Register(mux, "/v1alpha1/plugin/settings")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// TestSettingsFrontendRoundTripStripsSecret: a frontend token authorizes set/get,
// and the secret written is reported set but never returned (the settings negative,
// at the HTTP boundary).
func TestSettingsFrontendRoundTripStripsSecret(t *testing.T) {
	h, signer := settingsSetup(t)
	tok := frontendTok(t, signer, "acme/dash", "projA")

	rec := callSettings(h, "set", tok, `{"values":{"endpoint":"https://api.x","apiKey":"sk-secret-xyz","enabled":true}}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("set: %d %s", rec.Code, rec.Body.String())
	}

	rec = callSettings(h, "get", tok, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "sk-secret-xyz") {
		t.Fatalf("secret leaked in GET response: %s", body)
	}
	var view struct {
		Values  map[string]json.RawMessage `json:"values"`
		Secrets map[string]bool            `json:"secrets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Secrets["apiKey"] {
		t.Fatal("apiKey should be reported set")
	}
	if _, leaked := view.Values["apiKey"]; leaked {
		t.Fatal("secret must not be in values")
	}
	if string(view.Values["endpoint"]) != `"https://api.x"` {
		t.Fatalf("endpoint wrong: %s", view.Values["endpoint"])
	}
}

// TestSettingsCustomModeOpaqueAndSecret proves: a CUSTOM-mode plugin persists
// arbitrary NESTED (non-subset) settings the flat schema-form could never express, and its
// declared secret is still encrypted + never returned (the secret rule holds in custom mode).
func TestSettingsCustomModeOpaqueAndSecret(t *testing.T) {
	h, signer := settingsSetup(t)
	tok := frontendTok(t, signer, "acme/custom", "projA")

	// A rule-builder's output: a nested array/object the flat subset would reject, plus
	// the declared secret.
	rec := callSettings(h, "set", tok, `{"values":{
		"rules":[{"when":{"field":"status","op":"eq","value":"error"},"then":["alert","tag:urgent"]}],
		"layout":{"columns":3,"pinned":["latency"]},
		"apiKey":"sk-custom-9999"
	}}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("custom set: %d %s", rec.Code, rec.Body.String())
	}

	rec = callSettings(h, "get", tok, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("custom get: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The secret must NEVER appear in any GET response.
	if strings.Contains(body, "sk-custom-9999") {
		t.Fatalf("secret leaked in custom-mode GET: %s", body)
	}
	var view struct {
		Values  map[string]json.RawMessage `json:"values"`
		Secrets map[string]bool            `json:"secrets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	// The opaque nested values round-trip verbatim.
	if !strings.Contains(string(view.Values["rules"]), `"op":"eq"`) {
		t.Fatalf("nested rules not persisted opaquely: %s", view.Values["rules"])
	}
	if !strings.Contains(string(view.Values["layout"]), `"columns":3`) {
		t.Fatalf("nested layout not persisted: %s", view.Values["layout"])
	}
	// The secret is reported set but never in values.
	if !view.Secrets["apiKey"] {
		t.Fatal("apiKey should be reported set in custom mode")
	}
	if _, leaked := view.Values["apiKey"]; leaked {
		t.Fatal("secret must not be in custom-mode values")
	}
}

// TestSettingsWriteAuthorityIsPerTokenProject is the prove-the-negative for the
// cross-org settings-write escalation: the write-authority gate is evaluated against the
// TOKEN's project (c.ProjectID), so authority is resolved in that project's org — a user
// who lacks write authority in the token's project's org is denied, even if they hold it
// elsewhere (an ambient default-org role can never authorize a cross-org settings write).
func TestSettingsWriteAuthorityIsPerTokenProject(t *testing.T) {
	// canWrite grants authority ONLY for the project "projA" — modeling a user who is an
	// admin in projA's org but a viewer in projB's org.
	h, signer := settingsSetupAuthz(t, func(_ *http.Request, projectID string) bool {
		return projectID == "projA"
	})

	// A token for projA (authorized org) → the write is allowed.
	okTok := frontendTok(t, signer, "acme/dash", "projA")
	if rec := callSettings(h, "set", okTok, `{"values":{"endpoint":"https://a","apiKey":"k"}}`); rec.Code != http.StatusNoContent {
		t.Fatalf("write with authority in the token's project must succeed, got %d %s", rec.Code, rec.Body.String())
	}

	// A token for projB (a project in an org where the user is only a viewer) → DENIED,
	// because the gate resolves authority against projB, not the ambient default org.
	crossOrgTok := frontendTok(t, signer, "acme/dash", "projB")
	if rec := callSettings(h, "set", crossOrgTok, `{"values":{"endpoint":"https://b","apiKey":"k"}}`); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-org write (no authority in the token's project) must be 403, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestSettingsRejections(t *testing.T) {
	h, signer := settingsSetup(t)

	t.Run("no frontend token -> 401", func(t *testing.T) {
		if rec := callSettings(h, "get", "", `{}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", rec.Code)
		}
	})
	t.Run("plugin without schema -> 404", func(t *testing.T) {
		tok := frontendTok(t, signer, "acme/noschema", "projA")
		if rec := callSettings(h, "get", tok, `{}`); rec.Code != http.StatusNotFound {
			t.Fatalf("want 404, got %d", rec.Code)
		}
	})
	t.Run("invalid value -> 400", func(t *testing.T) {
		tok := frontendTok(t, signer, "acme/dash", "projA")
		// missing required endpoint
		if rec := callSettings(h, "set", tok, `{"values":{"apiKey":"k"}}`); rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("read-only viewer cannot write settings -> 403", func(t *testing.T) {
		// canWrite=false models a viewer (no configuration authority). The token is a
		// valid frontend token, so plugin/tenant scoping passes — only the write
		// authority gate stops it. Reads are still allowed.
		h, signer := settingsSetupAuthz(t, func(*http.Request, string) bool { return false })
		tok := frontendTok(t, signer, "acme/dash", "projA")
		if rec := callSettings(h, "set", tok, `{"values":{"endpoint":"https://x","apiKey":"k"}}`); rec.Code != http.StatusForbidden {
			t.Fatalf("a viewer must not write settings, want 403 got %d", rec.Code)
		}
		if rec := callSettings(h, "get", tok, `{}`); rec.Code != http.StatusOK {
			t.Fatalf("a viewer may still READ settings, want 200 got %d", rec.Code)
		}
	})
	t.Run("a backend double token is NOT accepted on the frontend settings seam", func(t *testing.T) {
		// A service token + identity assertion (the backend credential) must not
		// satisfy RequireFrontend — only a purpose-marked frontend token does.
		svc, _, _ := signer.MintServiceToken("acme/dash", nil, time.Now(), time.Minute)
		asr, _, _ := signer.MintIdentityAssertion("acme/dash", "u@x", "projA", "session:u@x", nil, time.Now(), time.Minute)
		r := httptest.NewRequest(http.MethodPost, "/v1alpha1/plugin/settings/get", strings.NewReader(`{}`))
		r.Header.Set("X-LLMObs-Service-Token", svc)
		r.Header.Set(pluginproto.IdentityAssertionHeader, asr)
		mux := http.NewServeMux()
		h.Register(mux, "/v1alpha1/plugin/settings")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("backend double token must not authorize the frontend settings seam, got %d", rec.Code)
		}
	})
}
