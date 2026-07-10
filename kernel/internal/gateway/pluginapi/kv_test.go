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

func newFakeKV() *fakeKV { return &fakeKV{m: map[string]json.RawMessage{}} }
func fk(p, pr, k string) string { return p + "\x00" + pr + "\x00" + k }

func (f *fakeKV) Get(_ context.Context, p, pr, k string) (json.RawMessage, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[fk(p, pr, k)]
	return v, ok, nil
}
func (f *fakeKV) Set(_ context.Context, p, pr, k string, v json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[fk(p, pr, k)] = v
	return nil
}
func (f *fakeKV) Delete(_ context.Context, p, pr, k string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, fk(p, pr, k))
	return nil
}
func (f *fakeKV) List(_ context.Context, p, pr, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.m {
		parts := strings.SplitN(k, "\x00", 3)
		if parts[0] == p && parts[1] == pr && strings.HasPrefix(parts[2], prefix) {
			out = append(out, parts[2])
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

// TestKVIsolation is the H4 proof: a plugin cannot read another plugin's kv, and
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
