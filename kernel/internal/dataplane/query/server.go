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
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/authhttp"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/queryapi"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// Server implements the generated Query API ServerInterface for the lite adapter.
// B1 implements POST /v1alpha1/query (spans target) and GET /spans/{id}; the rest
// return an honest 501 (documented as v-next in the OpenAPI).
type Server struct {
	store     storage.TelemetryStore
	pool      *pgxpool.Pool
	log       *slog.Logger
	maxWindow time.Duration
}

func NewServer(store storage.TelemetryStore, pool *pgxpool.Pool, log *slog.Logger, maxWindow time.Duration) *Server {
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
	// Browser callers (the shell + plugins) authenticate with a session cookie,
	// not a bearer. A resolved session grants the admin full scopes on the
	// selected project (single-admin lite; RBAC/project membership is future).
	if bearer == "" {
		if sess, ok := authhttp.SessionFrom(r.Context()); ok {
			projectID, err := s.sessionProject(r)
			if err != nil {
				return controlplane.Identity{}, errf("unauthorized", 403, "no project available")
			}
			_ = sess
			return controlplane.Identity{ProjectID: projectID, Scopes: []string{"ingest", "query", "scores:write", "delete"}}, nil
		}
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

// sessionProject resolves the project a browser session operates on: the
// X-LLMObs-Project header (the shell's project context) when present and valid,
// else the single default project (single-project lite).
func (s *Server) sessionProject(r *http.Request) (string, error) {
	if p := r.Header.Get("X-LLMObs-Project"); p != "" {
		return p, nil
	}
	return controlplane.DefaultProjectID(r.Context(), s.pool)
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
	if _, hasAgg := doc["aggregations"]; hasAgg {
		writeErr(w, errf("not_implemented", 501, "aggregations not implemented in v1alpha1/B1"))
		return
	}

	var c *Compiled
	var cerr error
	switch target {
	case "spans":
		c, cerr = CompileSpans(doc, id.ProjectID, s.maxWindow)
	case "traces":
		c, cerr = CompileTraces(doc, id.ProjectID, s.maxWindow)
	case "scores":
		c, cerr = CompileScores(doc, id.ProjectID, s.maxWindow)
	default:
		writeErr(w, errf("schema_invalid", 400, "target must be one of spans|traces|scores"))
		return
	}
	if cerr != nil {
		writeErr(w, cerr.(*CompileError))
		return
	}

	started := time.Now()
	var rows []json.RawMessage
	anchor := "start_time"
	switch target {
	case "traces":
		rows, err = s.store.QueryTraces(r.Context(), c.Where, c.Args, c.Order, c.Limit+1)
	case "scores":
		anchor = "timestamp"
		rows, err = s.store.QueryScores(r.Context(), c.Where, c.Args, c.Order, c.Limit+1)
	default:
		rows, err = s.store.QuerySpans(r.Context(), c.Where, c.Args, c.Order, c.Limit+1)
	}
	if err != nil {
		s.log.Error("query execution", "err", err.Error())
		writeErr(w, errf("internal", 500, "query failed"))
		return
	}

	var cursor string
	if len(rows) > c.Limit {
		rows = rows[:c.Limit]
		if st, id2, ok := cursorFromDoc(rows[len(rows)-1], anchor); ok {
			cursor = EncodeCursor(c.Fingerprint, st, id2)
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

// GetScore: GET /v1alpha1/scores/{id}.
func (s *Server) GetScore(w http.ResponseWriter, r *http.Request, id string) {
	ident, aerr := s.auth(r, "query")
	if aerr != nil {
		writeErr(w, aerr)
		return
	}
	doc, err := s.store.GetScore(r.Context(), ident.ProjectID, id)
	if err != nil {
		writeErr(w, errf("internal", 500, "fetch failed"))
		return
	}
	if doc == nil {
		writeErr(w, errf("not_found", 404, "no such score"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc)
}

// WriteScore: POST /v1alpha1/scores. Accepts one score object or an array. Writes
// are synchronous (not async-ack): 04-score.md §7 requires validation to be
// observable to the caller — an async ack would swallow the 400/422. Batch is
// all-or-nothing: any invalid score rejects the whole request before persisting.
func (s *Server) WriteScore(w http.ResponseWriter, r *http.Request) {
	ident, aerr := s.auth(r, "scores:write")
	if aerr != nil {
		writeErr(w, aerr)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	scores, perr := parseScoreBody(body)
	if perr != nil {
		writeErr(w, perr)
		return
	}
	if len(scores) == 0 {
		writeErr(w, errf("schema_invalid", 400, "no scores in request"))
		return
	}
	// Validate all before persisting any (observable, atomic).
	for _, sc := range scores {
		sc["project_id"] = ident.ProjectID
		if verr := validateScore(sc); verr != nil {
			writeErr(w, verr)
			return
		}
	}
	for _, sc := range scores {
		if err := s.store.PersistScore(r.Context(), scoreEvent(sc)); err != nil {
			s.log.Error("score persist", "err", err.Error())
			writeErr(w, errf("internal", 500, "score write failed"))
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"written": len(scores)})
}

func parseScoreBody(body []byte) ([]map[string]any, *CompileError) {
	trimmed := bytesTrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var arr []map[string]any
		if err := json.Unmarshal(body, &arr); err != nil {
			return nil, errf("schema_invalid", 400, "invalid JSON score array")
		}
		return arr, nil
	}
	var one map[string]any
	if err := json.Unmarshal(body, &one); err != nil {
		return nil, errf("schema_invalid", 400, "invalid JSON score")
	}
	return []map[string]any{one}, nil
}

func bytesTrimSpace(b []byte) []byte {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\n' || b[i] == '\t' || b[i] == '\r') {
		i++
	}
	return b[i:]
}

func cursorFromDoc(doc json.RawMessage, anchorField string) (time.Time, string, bool) {
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		return time.Time{}, "", false
	}
	id, _ := m["id"].(string)
	st, err := parseTime(m[anchorField])
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
