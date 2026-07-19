package pluginapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/bus"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginauth"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

func eventsSetup(t *testing.T) (*Events, *bus.Bus, *plugintoken.Signer) {
	t.Helper()
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	b := bus.New(bus.NewMemStore(), 1000)
	return NewEvents(pluginauth.New(signer, nil), b), b, signer
}

func evTokens(t *testing.T, signer *plugintoken.Signer, pluginID, projectID string, caps ...string) (svc, asr string) {
	t.Helper()
	now := time.Now()
	if len(caps) == 0 {
		caps = []string{perm.CapMarker("events")}
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

func callEvents(h *Events, op, svc, asr, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1alpha1/plugin/events/"+op, strings.NewReader(body))
	if svc != "" {
		r.Header.Set("X-LLMObs-Service-Token", svc)
	}
	if asr != "" {
		r.Header.Set(pluginproto.IdentityAssertionHeader, asr)
	}
	mux := http.NewServeMux()
	h.Register(mux, "/v1alpha1/plugin/events")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// TestEventsPollAckTenantScoped drives the events primitive end-to-end through the
// HTTP + double-token path: a plugin polls only its own tenant's events, and ack
// advances its offset. A different project sees nothing.
func TestEventsPollAckTenantScoped(t *testing.T) {
	h, b, signer := eventsSetup(t)
	ctx := context.Background()
	_ = b.Publish(ctx, "span.ingested", "projA", "a1")
	_ = b.Publish(ctx, "span.ingested", "projB", "b1")

	aSvc, aAsr := evTokens(t, signer, "acme/w", "projA")
	rec := callEvents(h, "poll", aSvc, aAsr, `{"topics":["span.ingested"],"max":10}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("poll: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Events []bus.Delivered `json:"events"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Events) != 1 || out.Events[0].SubjectID != "a1" {
		t.Fatalf("projA must see only its own event: %+v", out.Events)
	}

	// projB subscriber sees only b1 — tenant isolation through the handler.
	bSvc, bAsr := evTokens(t, signer, "acme/w", "projB")
	brec := callEvents(h, "poll", bSvc, bAsr, `{"topics":["span.ingested"],"max":10}`)
	if strings.Contains(brec.Body.String(), "a1") {
		t.Fatalf("projB leaked projA's event: %s", brec.Body.String())
	}

	// Ack projA up to the event id → no re-delivery.
	ackBody := `{"topic":"span.ingested","offset":` + itoaID(out.Events[0].ID) + `}`
	if arec := callEvents(h, "ack", aSvc, aAsr, ackBody); arec.Code != http.StatusNoContent {
		t.Fatalf("ack: %d %s", arec.Code, arec.Body.String())
	}
	empty := callEvents(h, "poll", aSvc, aAsr, `{"topics":["span.ingested"],"max":10}`)
	var out2 struct {
		Events []bus.Delivered `json:"events"`
	}
	_ = json.Unmarshal(empty.Body.Bytes(), &out2)
	if len(out2.Events) != 0 {
		t.Fatalf("acked events must not re-deliver, got %d", len(out2.Events))
	}
}

func TestEventsRequireCapability(t *testing.T) {
	h, _, signer := eventsSetup(t)
	svc, asr := evTokens(t, signer, "acme/w", "projA", perm.CapMarker("kv")) // no cap:events
	if rec := callEvents(h, "poll", svc, asr, `{"topics":["span.ingested"]}`); rec.Code != http.StatusForbidden {
		t.Fatalf("missing cap:events must be 403, got %d", rec.Code)
	}
	// fail is on the same capability seam as poll/ack.
	if rec := callEvents(h, "fail", svc, asr, `{"topic":"span.ingested","id":1,"reason":"x"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("missing cap:events must 403 the fail path too, got %d", rec.Code)
	}
}

// TestEventsFailDeadLettersPoison drives /fail through the HTTP + double-token path: a
// permanent poison at the head of the unacked window is dead-lettered and skipped so the
// good event behind it flows; failing an id ahead of the head is a 409.
func TestEventsFailDeadLettersPoison(t *testing.T) {
	h, b, signer := eventsSetup(t)
	ctx := context.Background()
	_ = b.Publish(ctx, "span.ingested", "projA", "e1")
	_ = b.Publish(ctx, "span.ingested", "projA", "poison")
	_ = b.Publish(ctx, "span.ingested", "projA", "e3")
	svc, asr := evTokens(t, signer, "acme/w", "projA")

	// Failing id 2 while the head is 1 must be a 409 (would skip e1).
	if rec := callEvents(h, "fail", svc, asr, `{"topic":"span.ingested","id":2,"reason":"early"}`); rec.Code != http.StatusConflict {
		t.Fatalf("failing ahead of head must be 409, got %d %s", rec.Code, rec.Body.String())
	}

	// Ack e1, then fail the poison at the head (id 2).
	if rec := callEvents(h, "ack", svc, asr, `{"topic":"span.ingested","offset":1}`); rec.Code != http.StatusNoContent {
		t.Fatalf("ack e1: %d", rec.Code)
	}
	if rec := callEvents(h, "fail", svc, asr, `{"topic":"span.ingested","id":2,"reason":"malformed subject"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("fail poison: %d %s", rec.Code, rec.Body.String())
	}

	// The good event behind the poison now flows; the poison never re-delivers.
	rec := callEvents(h, "poll", svc, asr, `{"topics":["span.ingested"],"max":10}`)
	var out struct {
		Events []bus.Delivered `json:"events"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Events) != 1 || out.Events[0].SubjectID != "e3" {
		t.Fatalf("after failing the poison, only e3 should remain: %+v", out.Events)
	}

	// Missing id is a 400.
	if bad := callEvents(h, "fail", svc, asr, `{"topic":"span.ingested"}`); bad.Code != http.StatusBadRequest {
		t.Fatalf("fail without id must be 400, got %d", bad.Code)
	}
}

func itoaID(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
