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
	"github.com/m7mdhka/llmobs/kernel/internal/plugindata"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// fakeStore is an in-memory StoreBackend keyed by (plugin, project, collection,
// id) — the same isolation the Postgres (project_id, id) primary key enforces, so
// the cross-tenant prove-the-negative runs at the authorization boundary without a
// database. Get/Delete/Query all key on project, so any leak would be the HANDLER
// passing the wrong scope.
type fakeStore struct {
	mu sync.Mutex
	m  map[string]json.RawMessage
}

func newFakeStore() *fakeStore         { return &fakeStore{m: map[string]json.RawMessage{}} }
func stk(p, pr, col, id string) string { return strings.Join([]string{p, pr, col, id}, "\x00") }

func (f *fakeStore) Put(_ context.Context, p, pr, col, id string, rec json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[stk(p, pr, col, id)] = rec
	return nil
}
func (f *fakeStore) Get(_ context.Context, p, pr, col, id string) (json.RawMessage, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[stk(p, pr, col, id)]
	return v, ok, nil
}
func (f *fakeStore) Query(_ context.Context, p, pr, col string, _ plugindata.Query) ([]json.RawMessage, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []json.RawMessage
	prefix := strings.Join([]string{p, pr, col}, "\x00") + "\x00"
	for k, v := range f.m {
		if strings.HasPrefix(k, prefix) {
			out = append(out, v)
		}
	}
	return out, "", nil
}
func (f *fakeStore) Delete(_ context.Context, p, pr, col, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, stk(p, pr, col, id))
	return nil
}

func storeSetup(t *testing.T) (*Store, *plugintoken.Signer) {
	t.Helper()
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(pluginauth.New(signer, nil), newFakeStore()), signer
}

func storeTokens(t *testing.T, signer *plugintoken.Signer, pluginID, projectID string, caps ...string) (svc, asr string) {
	t.Helper()
	now := time.Now()
	if len(caps) == 0 {
		caps = []string{perm.CapMarker("store")}
	}
	svc, _, err := signer.MintServiceToken(pluginID, caps, now, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	asr, _, err = signer.MintIdentityAssertion(pluginID, "u", projectID, "s", perm.All(), now, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return svc, asr
}

func callStore(h *Store, op, svc, asr, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1alpha1/plugin/store/"+op, strings.NewReader(body))
	if svc != "" {
		r.Header.Set("X-LLMObs-Service-Token", svc)
	}
	if asr != "" {
		r.Header.Set(pluginproto.IdentityAssertionHeader, asr)
	}
	mux := http.NewServeMux()
	h.Register(mux, "/v1alpha1/plugin/store")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// TestStoreCrossTenantIsolation is H5's prove-the-negative: a plugin scoped to
// project A cannot read (get OR query) project B's rows in its OWN collection —
// even with a valid token — because project_id comes from the assertion, never the
// body. Positive control: it CAN read its own project's rows.
func TestStoreCrossTenantIsolation(t *testing.T) {
	h, signer := storeSetup(t)

	// Plugin acme/w writes a row in project A.
	aSvc, aAsr := storeTokens(t, signer, "acme/w", "projA")
	if rec := callStore(h, "put", aSvc, aAsr, `{"collection":"dashboards","id":"d1","record":{"title":"A-only"}}`); rec.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}

	// The SAME plugin in project B must not see it (cross-tenant, the worst failure).
	bSvc, bAsr := storeTokens(t, signer, "acme/w", "projB")
	if rec := callStore(h, "get", bSvc, bAsr, `{"collection":"dashboards","id":"d1"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant GET must be 404, got %d: %s", rec.Code, rec.Body.String())
	}
	qrec := callStore(h, "query", bSvc, bAsr, `{"collection":"dashboards"}`)
	if strings.Contains(qrec.Body.String(), "A-only") {
		t.Fatalf("cross-tenant QUERY leaked project A's row: %s", qrec.Body.String())
	}

	// A body-supplied project_id must NOT override the assertion's tenant (defence
	// against a plugin trying to escape its tenant via the request body).
	spoof := callStore(h, "get", bSvc, bAsr, `{"collection":"dashboards","id":"d1","project_id":"projA"}`)
	if spoof.Code != http.StatusNotFound {
		t.Fatalf("body project_id must be ignored; still 404 expected, got %d", spoof.Code)
	}

	// Positive control: the owner in project A reads its own row.
	if rec := callStore(h, "get", aSvc, aAsr, `{"collection":"dashboards","id":"d1"}`); rec.Code != http.StatusOK {
		t.Fatalf("owner read in its own project must be 200, got %d", rec.Code)
	}
}

func TestStoreCrossPluginIsolation(t *testing.T) {
	h, signer := storeSetup(t)
	aSvc, aAsr := storeTokens(t, signer, "acme/a", "projA")
	callStore(h, "put", aSvc, aAsr, `{"collection":"c","id":"x","record":{"v":1}}`)
	// Different plugin, same project + collection + id → not its data.
	bSvc, bAsr := storeTokens(t, signer, "acme/b", "projA")
	if rec := callStore(h, "get", bSvc, bAsr, `{"collection":"c","id":"x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-plugin GET must be 404, got %d", rec.Code)
	}
}

func TestStoreRequiresCapability(t *testing.T) {
	h, signer := storeSetup(t)
	svc, asr := storeTokens(t, signer, "acme/w", "projA", perm.CapMarker("kv")) // no cap:store
	if rec := callStore(h, "put", svc, asr, `{"collection":"c","id":"x","record":{"v":1}}`); rec.Code != http.StatusForbidden {
		t.Fatalf("missing cap:store must be 403, got %d", rec.Code)
	}
}
