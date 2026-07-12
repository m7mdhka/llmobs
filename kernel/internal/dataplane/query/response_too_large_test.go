package query

// Prove-the-negative at the handler seam: when a DSL read refuses because the
// serialized result would exceed the response ceiling (storage.ErrResponseTooLarge),
// the Query API must answer a TYPED 413 `response_too_large` that tells the caller how
// to recover — NOT an opaque 500, and never an OOM. The adapter-level enforcement +
// cross-adapter parity (that BOTH real engines refuse at the same threshold) is proven
// against live DBs in internal/storage/crossadapter (TestCrossAdapterResponseBudget);
// this proves the mapping the handler applies to that refusal, hermetically.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// oversizeStore is a benign TelemetryStore whose list reads all refuse with
// ErrResponseTooLarge — standing in for a real adapter whose scan loop tripped the
// response budget. Every other method is a harmless zero so the handler reaches the
// list call.
type oversizeStore struct{}

func (oversizeStore) PersistSpan(context.Context, storage.Event) error  { return nil }
func (oversizeStore) PersistScore(context.Context, storage.Event) error { return nil }
func (oversizeStore) QuerySpans(context.Context, string, []any, string, int) ([]json.RawMessage, error) {
	return nil, storage.ErrResponseTooLarge
}
func (oversizeStore) QueryTraces(context.Context, string, []any, string, int) ([]json.RawMessage, error) {
	return nil, storage.ErrResponseTooLarge
}
func (oversizeStore) QueryScores(context.Context, string, []any, string, int) ([]json.RawMessage, error) {
	return nil, storage.ErrResponseTooLarge
}
func (oversizeStore) GetSpan(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}
func (oversizeStore) GetScore(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}
func (oversizeStore) GetTraceSpans(context.Context, string, string) ([]json.RawMessage, error) {
	return nil, storage.ErrResponseTooLarge
}
func (oversizeStore) EraseSpans(context.Context, string, string, string, time.Time, time.Time) (int, string, error) {
	return 0, "", nil
}
func (oversizeStore) QueryAggregation(context.Context, string, string, string, string, []any) ([]map[string]any, error) {
	return nil, nil
}

func newBudgetHarness(t *testing.T, signer *plugintoken.Signer) *Server {
	t.Helper()
	return &Server{
		store:            oversizeStore{},
		signer:           signer,
		dialect:          PostgresDialect,
		maxWindow:        365 * 24 * time.Hour,
		maxResponseBytes: DefaultMaxResponseBytes,
		log:              discardLogger(),
	}
}

func TestResponseTooLargeMapsTo413(t *testing.T) {
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	tr := map[string]any{
		"from": now.Add(-time.Hour).Format(time.RFC3339),
		"to":   now.Add(time.Hour).Format(time.RFC3339),
	}
	listBody := func(target string) []byte {
		b, _ := json.Marshal(map[string]any{"target": target, "timeRange": tr})
		return b
	}

	cases := []struct {
		name string
		run  func(s *Server, w *httptest.ResponseRecorder)
	}{
		{"list_spans", func(s *Server, w *httptest.ResponseRecorder) {
			s.RunQuery(w, authedReq(t, signer, http.MethodPost, "/v1alpha1/query", listBody("spans")))
		}},
		{"list_traces", func(s *Server, w *httptest.ResponseRecorder) {
			s.RunQuery(w, authedReq(t, signer, http.MethodPost, "/v1alpha1/query", listBody("traces")))
		}},
		{"list_scores", func(s *Server, w *httptest.ResponseRecorder) {
			s.RunQuery(w, authedReq(t, signer, http.MethodPost, "/v1alpha1/query", listBody("scores")))
		}},
		{"trace_tree", func(s *Server, w *httptest.ResponseRecorder) {
			s.GetTraceTree(w, authedReq(t, signer, http.MethodGet, "/v1alpha1/traces/x/tree", nil), "trace-1")
		}},
		{"get_trace", func(s *Server, w *httptest.ResponseRecorder) {
			s.GetTrace(w, authedReq(t, signer, http.MethodGet, "/v1alpha1/traces/x", nil), "trace-1")
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newBudgetHarness(t, signer)
			w := httptest.NewRecorder()
			c.run(s, w)

			if w.Code != http.StatusRequestEntityTooLarge { // 413
				t.Fatalf("want 413 for an oversized response, got %d (an OOM/500 would be the bug)", w.Code)
			}
			var body struct{ Code, Message string }
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if body.Code != "response_too_large" {
				t.Fatalf("want typed code response_too_large, got %q", body.Code)
			}
			// The message must be actionable (tell the caller to narrow/paginate), not opaque.
			if body.Message == "" {
				t.Fatal("response_too_large must carry a recovery hint, got empty message")
			}
		})
	}
}
