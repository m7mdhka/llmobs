package pluginapi

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginauth"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/secretbox"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// fakeSecretStore holds only ciphertext (as the Postgres table does), keyed by
// (plugin, project, name).
type fakeSecretStore struct {
	mu sync.Mutex
	ct map[string][]byte
	nc map[string][]byte
}

func newFakeSecretStore() *fakeSecretStore {
	return &fakeSecretStore{ct: map[string][]byte{}, nc: map[string][]byte{}}
}
func sk(p, pr, n string) string { return p + "\x00" + pr + "\x00" + n }

func (f *fakeSecretStore) SetEncrypted(_ context.Context, p, pr, n string, ct, nc []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ct[sk(p, pr, n)] = ct
	f.nc[sk(p, pr, n)] = nc
	return nil
}
func (f *fakeSecretStore) GetEncrypted(_ context.Context, p, pr, n string) ([]byte, []byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ct, ok := f.ct[sk(p, pr, n)]
	return ct, f.nc[sk(p, pr, n)], ok, nil
}
func (f *fakeSecretStore) ListNames(_ context.Context, p, pr string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.ct {
		parts := strings.SplitN(k, "\x00", 3)
		if parts[0] == p && parts[1] == pr {
			out = append(out, parts[2])
		}
	}
	return out, nil
}
func (f *fakeSecretStore) Delete(_ context.Context, p, pr, n string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.ct, sk(p, pr, n))
	delete(f.nc, sk(p, pr, n))
	return nil
}

// rawStored returns the bytes physically stored for a secret (to prove they are
// ciphertext, not plaintext).
func (f *fakeSecretStore) rawStored(p, pr, n string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ct[sk(p, pr, n)]
}

const plaintext = "sk-super-secret-value-9f8e7d"

func secretsSetup(t *testing.T) (*Secrets, *fakeSecretStore, *fakeKV, *plugintoken.Signer, *bytes.Buffer) {
	t.Helper()
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.NewRandom()
	if err != nil {
		t.Fatal(err)
	}
	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := newFakeSecretStore()
	authz := pluginauth.New(signer, nil)
	return NewSecrets(authz, store, box, logger), store, newFakeKV(), signer, logBuf
}

func secretTokens(t *testing.T, signer *plugintoken.Signer, pluginID, projectID string) (svc, asr string) {
	t.Helper()
	now := time.Now()
	svc, _, err := signer.MintServiceToken(pluginID, []string{perm.CapMarker("secrets"), perm.CapMarker("kv")}, now, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	asr, _, err = signer.MintIdentityAssertion(pluginID, "user-1", projectID, "session:u@x", perm.All(), now, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return svc, asr
}

func callSecrets(h *Secrets, op, svc, asr, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1alpha1/plugin/secrets/"+op, strings.NewReader(body))
	if svc != "" {
		r.Header.Set("X-LLMObs-Service-Token", svc)
	}
	if asr != "" {
		r.Header.Set(pluginproto.IdentityAssertionHeader, asr)
	}
	mux := http.NewServeMux()
	h.Register(mux, "/v1alpha1/plugin/secrets")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// TestSecretNeverReadable is the "prove the negative" acceptance test: after a
// secret is set, its plaintext value is returned by NO read path except the owning
// plugin's authenticated delivery. Every enumerated surface is exercised and the
// plaintext must appear in none of them.
func TestSecretNeverReadable(t *testing.T) {
	h, store, kvStore, signer, logBuf := secretsSetup(t)
	svc, asr := secretTokens(t, signer, "acme/widget", "projA")

	if rec := callSecrets(h, "set", svc, asr, `{"name":"openai_key","value":"`+plaintext+`"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("set failed: %d %s", rec.Code, rec.Body.String())
	}

	// Path 1 — the secrets LIST / settings view: names + set flags, never values.
	list := callSecrets(h, "list", svc, asr, `{}`)
	if strings.Contains(list.Body.String(), plaintext) {
		t.Fatalf("secrets list leaked the value: %s", list.Body.String())
	}
	if !strings.Contains(list.Body.String(), "openai_key") || !strings.Contains(list.Body.String(), `"set":true`) {
		t.Fatalf("list should show the name + set flag: %s", list.Body.String())
	}

	// Path 2 — kv.get for the same key: a different primitive/table, cannot reach it.
	kvHandler := NewKV(pluginauth.New(signer, nil), kvStore)
	kvRec := call(kvHandler, "get", svc, asr, `{"key":"openai_key"}`)
	if kvRec.Code != http.StatusNotFound || strings.Contains(kvRec.Body.String(), plaintext) {
		t.Fatalf("kv must not see the secret: %d %s", kvRec.Code, kvRec.Body.String())
	}

	// Path 3 — the physical store holds ciphertext, not plaintext.
	if raw := store.rawStored("acme/widget", "projA", "openai_key"); bytes.Contains(raw, []byte(plaintext)) {
		t.Fatal("stored bytes contain the plaintext — not encrypted at rest")
	}

	// Path 4 — error messages: a missing-secret get, and a cross-plugin get, must
	// not echo any plaintext.
	miss := callSecrets(h, "get", svc, asr, `{"name":"nope"}`)
	if strings.Contains(miss.Body.String(), plaintext) {
		t.Fatal("not-found error leaked the value")
	}
	bSvc, bAsr := secretTokens(t, signer, "acme/other", "projA")
	xrec := callSecrets(h, "get", bSvc, bAsr, `{"name":"openai_key"}`)
	if xrec.Code != http.StatusNotFound || strings.Contains(xrec.Body.String(), plaintext) {
		t.Fatalf("cross-plugin get must be 404 and leak nothing: %d %s", xrec.Code, xrec.Body.String())
	}

	// Path 5 — log lines: no operation logged the plaintext.
	if strings.Contains(logBuf.String(), plaintext) {
		t.Fatalf("a log line leaked the secret value: %s", logBuf.String())
	}

	// Positive control — the OWNING plugin's authenticated delivery DOES return it
	// (that is the point; it is not one of the forbidden read paths).
	deliver := callSecrets(h, "get", svc, asr, `{"name":"openai_key"}`)
	if deliver.Code != http.StatusOK || !strings.Contains(deliver.Body.String(), plaintext) {
		t.Fatalf("owner delivery must return the value: %d %s", deliver.Code, deliver.Body.String())
	}
}

func TestSecretsRequireCapability(t *testing.T) {
	h, _, _, signer, _ := secretsSetup(t)
	now := time.Now()
	// A plugin without cap:secrets (only cap:kv) is forbidden.
	svc, _, _ := signer.MintServiceToken("acme/widget", []string{perm.CapMarker("kv")}, now, 10*time.Minute)
	asr, _, _ := signer.MintIdentityAssertion("acme/widget", "u", "projA", "s", perm.All(), now, 5*time.Minute)
	if rec := callSecrets(h, "set", svc, asr, `{"name":"k","value":"v"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("missing cap:secrets must be 403, got %d", rec.Code)
	}
}
