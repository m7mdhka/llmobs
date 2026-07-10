package pluginapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/normalize"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/pipeline"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginauth"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// captureStore records persisted events so the test can inspect what the pipeline
// actually wrote (project + kernel-stamped source).
type captureStore struct {
	storage.TelemetryStore
	mu     sync.Mutex
	events []storage.Event
}

func (c *captureStore) PersistSpan(_ context.Context, ev storage.Event) error {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
	return nil
}

// A minimal OTLP/JSON trace with one span that FORGES an llmobs.source attribute.
const forgedOTLP = `{"resourceSpans":[{"scopeSpans":[{"spans":[{
	"traceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa01","spanId":"bbbbbbbbbbbbbb01","name":"gen",
	"startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000000001000000",
	"attributes":[{"key":"llmobs.source","value":{"stringValue":"plugin:evil/forged"}}]
}]}]}]}`

func ingestSetup(t *testing.T) (*Ingest, *captureStore, *plugintoken.Signer) {
	t.Helper()
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	cs := &captureStore{}
	pipe := pipeline.New(nil, cs, normalize.Default(), pipeline.NoopBus{}, pipeline.Config{})
	return NewIngest(pluginauth.New(signer, nil), pipe), cs, signer
}

func ingTokens(t *testing.T, signer *plugintoken.Signer, pluginID, projectID string, caps ...string) (svc, asr string) {
	t.Helper()
	now := time.Now()
	if len(caps) == 0 {
		caps = []string{perm.CapMarker("ingest")}
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

func postIngest(h *Ingest, svc, asr, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1alpha1/plugin/ingest/traces", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if svc != "" {
		r.Header.Set("X-LLMObs-Service-Token", svc)
	}
	if asr != "" {
		r.Header.Set(pluginproto.IdentityAssertionHeader, asr)
	}
	mux := http.NewServeMux()
	h.Register(mux, "/v1alpha1/plugin/ingest")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// TestIngestProveTheNegative: a cap:ingest plugin pushes spans through the SAME
// pipeline as OTLP, but CANNOT (1) forge its source — the kernel overwrites it with
// plugin:{id}; (2) write outside its project — the project is the caller's, from
// the assertion, never the body; (3) bypass the pipeline — the span is normalized
// to the canonical shape. Positive control: the span persists.
func TestIngestProveTheNegative(t *testing.T) {
	h, cs, signer := ingestSetup(t)
	svc, asr := ingTokens(t, signer, "acme/w", "projA")

	if rec := postIngest(h, svc, asr, forgedOTLP); rec.Code != http.StatusAccepted {
		t.Fatalf("ingest should accept, got %d: %s", rec.Code, rec.Body.String())
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.events) != 1 {
		t.Fatalf("expected 1 span through the pipeline, got %d", len(cs.events))
	}
	ev := cs.events[0]

	// (3) went through normalize — canonical shape with project_id + id.
	if ev.Payload["project_id"] != "projA" {
		t.Fatalf("(2) cross-project: span must land in the caller's project, got %v", ev.Payload["project_id"])
	}
	attrs, _ := ev.Payload["attributes"].(map[string]any)
	if attrs == nil {
		t.Fatal("(3) bypass: span was not normalized (no attributes)")
	}
	// (1) forged source overwritten by the kernel stamp.
	if attrs["llmobs.source"] != "plugin:acme/w" {
		t.Fatalf("(1) forge: source must be kernel-stamped plugin:acme/w, got %v", attrs["llmobs.source"])
	}
}

func TestIngestRequiresCapability(t *testing.T) {
	h, _, signer := ingestSetup(t)
	// A plugin without cap:ingest (only cap:query) is forbidden.
	svc, asr := ingTokens(t, signer, "acme/w", "projA", perm.CapMarker("query"))
	if rec := postIngest(h, svc, asr, forgedOTLP); rec.Code != http.StatusForbidden {
		t.Fatalf("missing cap:ingest must be 403, got %d", rec.Code)
	}
}

func TestIngestRequiresDoubleToken(t *testing.T) {
	h, _, signer := ingestSetup(t)
	svc, asr := ingTokens(t, signer, "acme/w", "projA")
	if rec := postIngest(h, svc, "", forgedOTLP); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing assertion must be 401, got %d", rec.Code)
	}
	if rec := postIngest(h, "", asr, forgedOTLP); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing service token must be 401, got %d", rec.Code)
	}
}
