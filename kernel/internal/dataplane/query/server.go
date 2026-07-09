package query

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/queryapi"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

// Server implements the generated Query API ServerInterface for the lite adapter.
// B1 implements POST /v1alpha1/query (spans target) and GET /spans/{id}; the rest
// return an honest 501 (documented as v-next in the OpenAPI).
type Server struct {
	store     *postgres.Store
	pool      *pgxpool.Pool
	log       *slog.Logger
	maxWindow time.Duration
}

func NewServer(store *postgres.Store, pool *pgxpool.Pool, log *slog.Logger, maxWindow time.Duration) *Server {
	return &Server{store: store, pool: pool, log: log, maxWindow: maxWindow}
}

// Handler returns the routed Query API handler (generated routing).
func (s *Server) Handler() http.Handler { return queryapi.Handler(s) }

var _ queryapi.ServerInterface = (*Server)(nil)

func (s *Server) auth(r *http.Request, scope string) (controlplane.Identity, *CompileError) {
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if bearer == "" {
		bearer = r.Header.Get("X-LLMObs-Service-Token") // B2 wires the double-token model
	}
	id, err := controlplane.Authenticate(r.Context(), s.pool, bearer)
	if err != nil {
		return controlplane.Identity{}, errf("unauthorized", 403, "invalid credentials")
	}
	if !id.HasScope(scope) {
		return controlplane.Identity{}, errf("unauthorized", 403, "missing scope %s", scope)
	}
	return id, nil
}

// RunQuery: POST /v1alpha1/query
func (s *Server) RunQuery(w http.ResponseWriter, r *http.Request) {
	id, aerr := s.auth(r, "query")
	if aerr != nil {
		writeErr(w, aerr)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	doc, err := jsonBody(body)
	if err != nil {
		writeErr(w, errf("schema_invalid", 400, "invalid JSON"))
		return
	}
	target, _ := doc["target"].(string)
	switch target {
	case "spans":
		// implemented below
	case "traces", "scores":
		writeErr(w, errf("not_implemented", 501, "target %q not implemented in v1alpha1/B1", target))
		return
	default:
		writeErr(w, errf("schema_invalid", 400, "target must be one of spans|traces|scores"))
		return
	}
	if _, hasAgg := doc["aggregations"]; hasAgg {
		writeErr(w, errf("not_implemented", 501, "aggregations not implemented in v1alpha1/B1"))
		return
	}

	c, cerr := CompileSpans(doc, id.ProjectID, s.maxWindow)
	if cerr != nil {
		writeErr(w, cerr.(*CompileError))
		return
	}
	started := time.Now()
	rows, err := s.store.QuerySpans(r.Context(), c.Where, c.Args, c.Order, c.Limit+1)
	if err != nil {
		s.log.Error("query execution", "err", err.Error())
		writeErr(w, errf("internal", 500, "query failed"))
		return
	}

	var cursor string
	if len(rows) > c.Limit {
		rows = rows[:c.Limit]
		if st, id2, ok := cursorFromDoc(rows[len(rows)-1]); ok {
			cursor = EncodeCursor(st, id2)
		}
	}

	data := make([]json.RawMessage, len(rows))
	copy(data, rows)
	resp := map[string]any{
		"version":  "v1alpha1",
		"data":     data,
		"stats":    map[string]any{"elapsed_ms": time.Since(started).Milliseconds(), "returned": len(rows)},
		"warnings": []any{},
	}
	if cursor != "" {
		resp["cursor"] = cursor
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetSpan: GET /v1alpha1/spans/{id}
func (s *Server) GetSpan(w http.ResponseWriter, r *http.Request, id string) {
	ident, aerr := s.auth(r, "query")
	if aerr != nil {
		writeErr(w, aerr)
		return
	}
	doc, err := s.store.GetSpan(r.Context(), ident.ProjectID, id)
	if err != nil {
		writeErr(w, errf("internal", 500, "fetch failed"))
		return
	}
	if doc == nil {
		writeErr(w, errf("not_found", 404, "no such span"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc)
}

// v-next endpoints (B2): honest 501.
func (s *Server) GetTraceTree(w http.ResponseWriter, r *http.Request, _ string) {
	writeErr(w, errf("not_implemented", 501, "trace tree fetch is v-next (B2)"))
}
func (s *Server) GetTrace(w http.ResponseWriter, r *http.Request, _ string) {
	writeErr(w, errf("not_implemented", 501, "traces target is v-next (B2)"))
}
func (s *Server) GetScore(w http.ResponseWriter, r *http.Request, _ string) {
	writeErr(w, errf("not_implemented", 501, "scores are v-next"))
}
func (s *Server) WriteScore(w http.ResponseWriter, r *http.Request) {
	writeErr(w, errf("not_implemented", 501, "score write path is v-next"))
}

func cursorFromDoc(doc json.RawMessage) (time.Time, string, bool) {
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		return time.Time{}, "", false
	}
	id, _ := m["id"].(string)
	st, err := parseTime(m["start_time"])
	if err != nil || id == "" {
		return time.Time{}, "", false
	}
	return st, id, true
}

func writeErr(w http.ResponseWriter, e *CompileError) {
	writeJSON(w, e.Status, map[string]any{"code": e.Code, "message": e.Msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
