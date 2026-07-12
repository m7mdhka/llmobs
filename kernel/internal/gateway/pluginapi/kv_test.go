package pluginapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginauth"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// fakeKV is an in-memory KVStore keyed by (plugin, project, key) — the same
// isolation the Postgres primary key enforces, so the handler-level isolation
// proof does not need a database.
type fakeKV struct {
	mu sync.Mutex
	m  map[string]json.RawMessage
}

func newFakeKV() *fakeKV           { return &fakeKV{m: map[string]json.RawMessage{}} }
func fk(p, pr, u, k string) string { return p + "\x00" + pr + "\x00" + u + "\x00" + k }

func (f *fakeKV) Get(_ context.Context, p, pr, u, k string) (json.RawMessage, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[fk(p, pr, u, k)]
	return v, ok, nil
}
func (f *fakeKV) Set(_ context.Context, p, pr, u, k string, v json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[fk(p, pr, u, k)] = v
	return nil
}
func (f *fakeKV) Delete(_ context.Context, p, pr, u, k string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, fk(p, pr, u, k))
	return nil
}
func (f *fakeKV) List(_ context.Context, p, pr, u, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.m {
		parts := strings.SplitN(k, "\x00", 4)
		if parts[0] == p && parts[1] == pr && parts[2] == u && strings.HasPrefix(parts[3], prefix) {
			out = append(out, parts[3])
		}
	}
	return out, nil
}

func setup(t *testing.T) (*KV, *plugintoken.Signer) {
	t.Helper()
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	return NewKV(pluginauth.New(signer, nil), newFakeKV()), signer
}

// tokens mints a service token (with cap:kv unless capOverride given) + an
// assertion for the plugin, on the given project.
func tokens(t *testing.T, signer *plugintoken.Signer, pluginID, projectID string, caps ...string) (svc, asr string) {
	t.Helper()
	now := time.Now()
	if len(caps) == 0 {
		caps = []string{perm.CapMarker("kv")}
	}
	svc, _, err := signer.MintServiceToken(pluginID, caps, now, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	asr, _, err = signer.MintIdentityAssertion(pluginID, "user-1", projectID, "session:u@x", perm.All(), now, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return svc, asr
}

func call(h *KV, op, svc, asr, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1alpha1/plugin/kv/"+op, strings.NewReader(body))
	if svc != "" {
		r.Header.Set("X-LLMObs-Service-Token", svc)
	}
	if asr != "" {
		r.Header.Set(pluginproto.IdentityAssertionHeader, asr)
	}
	mux := http.NewServeMux()
	h.Register(mux, "/v1alpha1/plugin/kv")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func TestKVRoundTrip(t *testing.T) {
	h, signer := setup(t)
	svc, asr := tokens(t, signer, "acme/widget", "projA")
	if rec := call(h, "set", svc, asr, `{"key":"layout","value":{"cols":3}}`); rec.Code != http.StatusNoContent {
		t.Fatalf("set: %d %s", rec.Code, rec.Body.String())
	}
	rec := call(h, "get", svc, asr, `{"key":"layout"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	var got struct{ Value map[string]int }
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Value["cols"] != 3 {
		t.Fatalf("value round-trip failed: %s", rec.Body.String())
	}
}

// TestKVIsolation proves: a plugin cannot read another plugin's kv, and
// a project cannot read another project's kv — even with valid tokens.
func TestKVIsolation(t *testing.T) {
	h, signer := setup(t)
	aSvc, aAsr := tokens(t, signer, "acme/a", "projA")
	call(h, "set", aSvc, aAsr, `{"key":"k","value":"a-secret"}`)

	// Different plugin, same project, same key -> not found (plugin isolation).
	bSvc, bAsr := tokens(t, signer, "acme/b", "projA")
	if rec := call(h, "get", bSvc, bAsr, `{"key":"k"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-plugin read must be 404, got %d: %s", rec.Code, rec.Body.String())
	}

	// Same plugin, different project, same key -> not found (tenant isolation).
	aSvcB, aAsrB := tokens(t, signer, "acme/a", "projB")
	if rec := call(h, "get", aSvcB, aAsrB, `{"key":"k"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-project read must be 404, got %d", rec.Code)
	}

	// The owner still reads its own value.
	if rec := call(h, "get", aSvc, aAsr, `{"key":"k"}`); rec.Code != http.StatusOK {
		t.Fatalf("owner read should be 200, got %d", rec.Code)
	}
}

// tokensSub is tokens() but with an explicit assertion subject (the acting user), so the
// per-user scope tests can act as different users.
func tokensSub(t *testing.T, signer *plugintoken.Signer, pluginID, projectID, subject string) (svc, asr string) {
	t.Helper()
	now := time.Now()
	svc, _, err := signer.MintServiceToken(pluginID, []string{perm.CapMarker("kv")}, now, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	asr, _, err = signer.MintIdentityAssertion(pluginID, subject, projectID, "session:"+subject, perm.All(), now, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return svc, asr
}

// TestKVUserScopeIsolation is the prove-the-negative: within the SAME plugin+project, one
// user's per-user (scope="user") state is invisible and unwritable to another user — because
// the store keys on the caller's VERIFIED subject, never a client-supplied field. Project scope
// stays shared. A user-less credential cannot use per-user scope.
func TestKVUserScopeIsolation(t *testing.T) {
	h, signer := setup(t)
	aSvc, aAsr := tokensSub(t, signer, "acme/w", "projA", "alice@x")
	bSvc, bAsr := tokensSub(t, signer, "acme/w", "projA", "bob@x")

	// Alice writes her per-user value.
	if rec := call(h, "set", aSvc, aAsr, `{"scope":"user","key":"prefs","value":{"theme":"dark"}}`); rec.Code != http.StatusNoContent {
		t.Fatalf("alice user-set: %d %s", rec.Code, rec.Body.String())
	}
	// Bob CANNOT read Alice's per-user value (same plugin, same project, same key).
	if rec := call(h, "get", bSvc, bAsr, `{"scope":"user","key":"prefs"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("bob must NOT read alice's per-user state, got %d %s", rec.Code, rec.Body.String())
	}
	// Bob writing "the same key" writes HIS OWN bucket — it does not overwrite Alice's.
	if rec := call(h, "set", bSvc, bAsr, `{"scope":"user","key":"prefs","value":{"theme":"light"}}`); rec.Code != http.StatusNoContent {
		t.Fatalf("bob user-set: %d", rec.Code)
	}
	rec := call(h, "get", aSvc, aAsr, `{"scope":"user","key":"prefs"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "dark") {
		t.Fatalf("alice must still read HER value (dark), got %d %s", rec.Code, rec.Body.String())
	}

	// Per-user state is a different bucket from project state: a user-scoped key is not visible
	// at project scope.
	if rec := call(h, "get", aSvc, aAsr, `{"scope":"project","key":"prefs"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("alice's user-scoped key must not appear at project scope, got %d", rec.Code)
	}
	// Project scope IS shared: Alice writes project-scoped, Bob reads it.
	if rec := call(h, "set", aSvc, aAsr, `{"scope":"project","key":"shared","value":1}`); rec.Code != http.StatusNoContent {
		t.Fatalf("project set: %d", rec.Code)
	}
	if rec := call(h, "get", bSvc, bAsr, `{"scope":"project","key":"shared"}`); rec.Code != http.StatusOK {
		t.Fatalf("project scope must be shared across users, got %d", rec.Code)
	}

	// List is scoped too: Alice's user-scope list shows only her keys, not Bob's or project's.
	rec = call(h, "list", aSvc, aAsr, `{"scope":"user","prefix":""}`)
	if !strings.Contains(rec.Body.String(), "prefs") || strings.Contains(rec.Body.String(), "shared") {
		t.Fatalf("alice user-list must show her keys only, got %s", rec.Body.String())
	}
}

// TestKVReservedKeyOpaque: the settings namespace ("__"-prefixed keys) is not reachable via
// the raw kv surface — a plugin can't read/write/delete or even see its own settings document
// through kv (defense-in-depth for the shared plugin_kv namespace).
func TestKVReservedKeyOpaque(t *testing.T) {
	h, signer := setup(t)
	svc, asr := tokens(t, signer, "acme/w", "projA")
	for _, op := range []struct{ name, body string }{
		{"get", `{"key":"__settings__"}`},
		{"set", `{"key":"__settings__","value":{"x":1}}`},
		{"delete", `{"key":"__x"}`},
	} {
		if rec := call(h, op.name, svc, asr, op.body); rec.Code != http.StatusForbidden {
			t.Fatalf("%s of a reserved key must be 403, got %d", op.name, rec.Code)
		}
	}
	// A normal key still works, and a reserved key never appears in a list.
	call(h, "set", svc, asr, `{"key":"normal","value":1}`)
	rec := call(h, "list", svc, asr, `{"prefix":""}`)
	if !strings.Contains(rec.Body.String(), "normal") || strings.Contains(rec.Body.String(), "__") {
		t.Fatalf("list must show normal keys but never reserved ones, got %s", rec.Body.String())
	}
}

func TestKVRequiresCapability(t *testing.T) {
	h, signer := setup(t)
	// A plugin without cap:kv (only cap:query) is forbidden.
	svc, asr := tokens(t, signer, "acme/widget", "projA", perm.CapMarker("query"))
	if rec := call(h, "set", svc, asr, `{"key":"k","value":1}`); rec.Code != http.StatusForbidden {
		t.Fatalf("missing cap:kv must be 403, got %d", rec.Code)
	}
}

func TestKVRequiresDoubleToken(t *testing.T) {
	h, signer := setup(t)
	svc, asr := tokens(t, signer, "acme/widget", "projA")
	// Missing assertion.
	if rec := call(h, "get", svc, "", `{"key":"k"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing assertion must be 401, got %d", rec.Code)
	}
	// Missing service token.
	if rec := call(h, "get", "", asr, `{"key":"k"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing service token must be 401, got %d", rec.Code)
	}
}
