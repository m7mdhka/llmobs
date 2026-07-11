package query

// R-MIG4 prove-the-negative: when dual-read is enabled, EVERY read path through the
// Query API resolves against the unified lite∪scale view — nothing reads a single
// adapter directly. This is the Langfuse #14827 trap (a forgotten hasAnyTrace()
// existence check queried the OLD table, so a migrating user saw an empty product).
//
// The proof is behavioral, not a grep: the Server's own s.store is a trapStore that
// FAILS the test the instant any of its methods is touched. The dual store wraps two
// recording backends (lite, scale). We drive every read/erase/score handler over
// HTTP with an authorized request and assert (a) the trap never fired — no handler
// bypassed the seam to hit a single adapter — and (b) BOTH backends were consulted,
// so the unified view is genuinely the union. A new read handler that forgets the
// seam and calls s.store.* directly trips the trap and fails this test by
// construction (invariant #11: the ONE convergence seam).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/dualstore"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// trapStore is a storage.TelemetryStore that fails the test if ANY method is called.
// It is installed as the Server's single store; in dual-read mode nothing may touch
// it — every read goes through the dual seam.
type trapStore struct{ t *testing.T }

func (s trapStore) fail(m string) {
	s.t.Helper()
	s.t.Fatalf("read bypassed the dual seam: single adapter %s was called directly (R-MIG4 violation)", m)
}

func (s trapStore) QuerySpans(context.Context, string, []any, string, int) ([]json.RawMessage, error) {
	s.fail("QuerySpans")
	return nil, nil
}
func (s trapStore) QueryTraces(context.Context, string, []any, string, int) ([]json.RawMessage, error) {
	s.fail("QueryTraces")
	return nil, nil
}
func (s trapStore) QueryScores(context.Context, string, []any, string, int) ([]json.RawMessage, error) {
	s.fail("QueryScores")
	return nil, nil
}
func (s trapStore) QueryAggregation(context.Context, string, string, string, string, []any) ([]map[string]any, error) {
	s.fail("QueryAggregation")
	return nil, nil
}
func (s trapStore) GetSpan(context.Context, string, string) (json.RawMessage, error) {
	s.fail("GetSpan")
	return nil, nil
}
func (s trapStore) GetScore(context.Context, string, string) (json.RawMessage, error) {
	s.fail("GetScore")
	return nil, nil
}
func (s trapStore) GetTraceSpans(context.Context, string, string) ([]json.RawMessage, error) {
	s.fail("GetTraceSpans")
	return nil, nil
}
func (s trapStore) PersistSpan(context.Context, storage.Event) error {
	s.fail("PersistSpan")
	return nil
}
func (s trapStore) PersistScore(context.Context, storage.Event) error {
	s.fail("PersistScore")
	return nil
}
func (s trapStore) EraseSpans(context.Context, string, string, string, time.Time, time.Time) (int, string, error) {
	s.fail("EraseSpans")
	return 0, "", nil
}

// recordStore records which read methods were exercised and returns benign empty
// results. Two of these back the dual store (lite + scale).
type recordStore struct {
	mu   sync.Mutex
	seen map[string]int
}

func newRecordStore() *recordStore { return &recordStore{seen: map[string]int{}} }
func (s *recordStore) mark(m string) {
	s.mu.Lock()
	s.seen[m]++
	s.mu.Unlock()
}
func (s *recordStore) hit(m string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[m] > 0
}

func (s *recordStore) QuerySpans(context.Context, string, []any, string, int) ([]json.RawMessage, error) {
	s.mark("QuerySpans")
	return nil, nil
}
func (s *recordStore) QueryTraces(context.Context, string, []any, string, int) ([]json.RawMessage, error) {
	s.mark("QueryTraces")
	return nil, nil
}
func (s *recordStore) QueryScores(context.Context, string, []any, string, int) ([]json.RawMessage, error) {
	s.mark("QueryScores")
	return nil, nil
}
func (s *recordStore) QueryAggregation(context.Context, string, string, string, string, []any) ([]map[string]any, error) {
	s.mark("QueryAggregation")
	return nil, nil
}
func (s *recordStore) GetSpan(context.Context, string, string) (json.RawMessage, error) {
	s.mark("GetSpan")
	return nil, nil
}
func (s *recordStore) GetScore(context.Context, string, string) (json.RawMessage, error) {
	s.mark("GetScore")
	return nil, nil
}
func (s *recordStore) GetTraceSpans(context.Context, string, string) ([]json.RawMessage, error) {
	s.mark("GetTraceSpans")
	return nil, nil
}
func (s *recordStore) PersistSpan(context.Context, storage.Event) error {
	s.mark("PersistSpan")
	return nil
}
func (s *recordStore) PersistScore(context.Context, storage.Event) error {
	s.mark("PersistScore")
	return nil
}
func (s *recordStore) EraseSpans(context.Context, string, string, string, time.Time, time.Time) (int, string, error) {
	s.mark("EraseSpans")
	return 0, "", nil
}

// The dual store's fail-closed erase requires these capabilities on lite/scale.
func (s *recordStore) SpanIDsForErase(context.Context, string, string, time.Time, time.Time) ([]string, error) {
	s.mark("SpanIDsForErase")
	return nil, nil
}
func (s *recordStore) SuppressSpans(context.Context, string, []string, string) error {
	s.mark("SuppressSpans")
	return nil
}

// authedReq builds a plugin→kernel request with full plugin+user scopes on a project.
func authedReq(t *testing.T, signer *plugintoken.Signer, method, path string, body []byte) *http.Request {
	t.Helper()
	now := time.Now()
	// Full data perms + the capability markers a plugin needs for read + score-write
	// ops (delete has no plugin capability by design, so erase is covered separately
	// via the reads() seam assertion below).
	pluginScopes := append(perm.All(),
		perm.CapMarker(perm.CapForOp("query")),
		perm.CapMarker(perm.CapForOp("scores:write")),
	)
	svc, _, err := signer.MintServiceToken("acme/a", pluginScopes, now, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	asr, _, err := signer.MintIdentityAssertion("acme/a", "user-1", "projA", "session:u@x", perm.All(), now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	r := httptest.NewRequest(method, path, reader)
	r.Header.Set("X-LLMObs-Service-Token", svc)
	r.Header.Set(pluginproto.IdentityAssertionHeader, asr)
	return r
}

// newSeamHarness builds a Server with a trapStore single adapter and a dual store
// over two FRESH recording backends — one per case, so a "hit" can never be a stale
// carry-over from an earlier case.
func newSeamHarness(t *testing.T, signer *plugintoken.Signer) (*Server, *recordStore, *recordStore) {
	t.Helper()
	lite, scale := newRecordStore(), newRecordStore()
	s := &Server{
		store:     trapStore{t: t}, // MUST NOT be touched in dual mode
		signer:    signer,
		dialect:   PostgresDialect,
		maxWindow: 365 * 24 * time.Hour,
		log:       discardLogger(),
	}
	s.SetDualStore(dualstore.New(lite, scale))
	return s, lite, scale
}

// TestDualSeamCoversEveryReadPath is the R-MIG4 prove-the-negative.
func TestDualSeamCoversEveryReadPath(t *testing.T) {
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	tr := map[string]any{
		"from": now.Add(-1 * time.Hour).Format(time.RFC3339),
		"to":   now.Add(1 * time.Hour).Format(time.RFC3339),
	}
	listBody := func(target string) []byte {
		b, _ := json.Marshal(map[string]any{"target": target, "timeRange": tr})
		return b
	}
	aggBody := func(target string) []byte {
		b, _ := json.Marshal(map[string]any{
			"target": target, "timeRange": tr,
			"aggregations": []any{map[string]any{"op": "count", "as": "n"}},
		})
		return b
	}
	validScore, _ := json.Marshal(map[string]any{
		"id": "score-1", "subject_id": "t1", "subject_type": "trace", "name": "quality",
		"timestamp": now.Format(time.RFC3339Nano), "data_type": "numeric", "value_numeric": 1.0, "source": "eval",
	})

	// Every read handler, driven over HTTP against a FRESH harness. After each, assert
	// the trap did not fire (checked continuously inside trapStore) AND both backends
	// were consulted (the read genuinely unified lite∪scale).
	cases := []struct {
		name       string
		run        func(s *Server)
		liteM, scM string // a backend method each store must have seen
	}{
		{"list_spans", func(s *Server) {
			s.RunQuery(httptest.NewRecorder(), authedReq(t, signer, http.MethodPost, "/v1alpha1/query", listBody("spans")))
		}, "QuerySpans", "QuerySpans"},
		{"list_traces", func(s *Server) {
			s.RunQuery(httptest.NewRecorder(), authedReq(t, signer, http.MethodPost, "/v1alpha1/query", listBody("traces")))
		}, "QueryTraces", "QueryTraces"},
		{"list_scores", func(s *Server) {
			s.RunQuery(httptest.NewRecorder(), authedReq(t, signer, http.MethodPost, "/v1alpha1/query", listBody("scores")))
		}, "QueryScores", "QueryScores"},
		{"aggregation", func(s *Server) {
			s.RunQuery(httptest.NewRecorder(), authedReq(t, signer, http.MethodPost, "/v1alpha1/query", aggBody("spans")))
		}, "QueryAggregation", "QueryAggregation"},
		{"get_span", func(s *Server) {
			s.GetSpan(httptest.NewRecorder(), authedReq(t, signer, http.MethodGet, "/v1alpha1/spans/x", nil), "span-1")
		}, "GetSpan", "GetSpan"},
		{"get_score", func(s *Server) {
			s.GetScore(httptest.NewRecorder(), authedReq(t, signer, http.MethodGet, "/v1alpha1/scores/x", nil), "score-1")
		}, "GetScore", "GetScore"},
		{"trace_tree", func(s *Server) {
			s.GetTraceTree(httptest.NewRecorder(), authedReq(t, signer, http.MethodGet, "/v1alpha1/traces/x/tree", nil), "trace-1")
		}, "GetTraceSpans", "GetTraceSpans"},
		{"get_trace", func(s *Server) {
			s.GetTrace(httptest.NewRecorder(), authedReq(t, signer, http.MethodGet, "/v1alpha1/traces/x", nil), "trace-1")
		}, "GetTraceSpans", "GetTraceSpans"},
		{"write_score", func(s *Server) {
			s.WriteScore(httptest.NewRecorder(), authedReq(t, signer, http.MethodPost, "/v1alpha1/scores", validScore))
		}, "GetScore", "PersistScore"}, // dual score write: lite GetScore (seed check) + scale PersistScore
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, lite, scale := newSeamHarness(t, signer)
			c.run(s)
			if !lite.hit(c.liteM) {
				t.Errorf("%s: lite backend %s not consulted — read did not unify lite∪scale", c.name, c.liteM)
			}
			if !scale.hit(c.scM) {
				t.Errorf("%s: scale backend %s not consulted — read did not unify lite∪scale", c.name, c.scM)
			}
		})
	}

	// Erasure is session/apikey-only (plugins hold no delete capability), so it can't
	// be driven through the plugin-token path above. Its handler's ONLY store access
	// is s.reads().EraseSpans — so prove the seam directly: reads() returns the dual
	// store (never the trap s.store), and dual.EraseSpans fans to BOTH backends. A
	// handler that erased via s.store directly would trip the trap.
	t.Run("erase_via_reads_seam", func(t *testing.T) {
		s, lite, scale := newSeamHarness(t, signer)
		if _, ok := s.reads().(*dualstore.Store); !ok {
			t.Fatal("reads() must return the dual store when dual-read is enabled")
		}
		if _, _, err := s.reads().EraseSpans(context.Background(), "projA", "u", "session:x", now.Add(-time.Hour), now); err != nil {
			t.Fatal(err)
		}
		if !lite.hit("EraseSpans") || !scale.hit("EraseSpans") {
			t.Fatal("erase must fan to BOTH backends through the dual seam")
		}
	})
}

// TestNoDirectSingleAdapterReads is the static grep-proof twin of the behavioral
// trap above: it scans the query package source and asserts NO handler reads a single
// adapter directly. Point reads/erase/score-ingest go through s.reads(); compiled-SQL
// list/aggregation reads go through the s.listRows/s.aggRows seam in dual.go. The
// direct s.store.Query*/Get*/Erase/PersistScore calls may therefore appear ONLY in
// dual.go (the one seam file), each guarded by `if s.dual != nil`. A new handler that
// calls the single store directly trips this even if nobody writes a behavioral test.
func TestNoDirectSingleAdapterReads(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	const seamFile = "dual.go" // the ONE place the single-store fallback may live
	direct := []string{
		"s.store.QuerySpans", "s.store.QueryTraces", "s.store.QueryScores", "s.store.QueryAggregation",
		"s.store.GetSpan", "s.store.GetScore", "s.store.GetTraceSpans",
		"s.store.EraseSpans", "s.store.PersistScore",
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == seamFile {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		for _, d := range direct {
			if strings.Contains(text, d) {
				t.Errorf("%s calls %s directly — reads must go through the s.reads()/s.listRows/s.aggRows seam (dual.go) so dual-read intercepts them (R-MIG4/#11)", f, d)
			}
		}
	}

	// In the seam file, every direct single-store read must sit under an `if s.dual != nil`
	// guard, proving it is the single-store fallback and not an unconditional read.
	src, err := os.ReadFile(seamFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, q := range []string{"s.store.QuerySpans", "s.store.QueryTraces", "s.store.QueryScores", "s.store.QueryAggregation"} {
		idx := strings.Index(text, q)
		if idx < 0 {
			continue
		}
		lo := idx - 600
		if lo < 0 {
			lo = 0
		}
		if !strings.Contains(text[lo:idx], "if s.dual != nil") {
			t.Errorf("%s: %s is not guarded by `if s.dual != nil` — a direct single-adapter read bypasses dual-read (R-MIG4)", seamFile, q)
		}
	}
}
