package query

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/authhttp"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/queryapi"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/metrics"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/dualstore"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// Server implements the generated Query API ServerInterface for the lite adapter.
// B1 implements POST /v1alpha1/query (spans target) and GET /spans/{id}; the rest
// return an honest 501 (documented as v-next in the OpenAPI).
type Server struct {
	store     storage.TelemetryStore
	pool      *pgxpool.Pool
	log       *slog.Logger
	maxWindow time.Duration
	metrics   *metrics.Registry
	signer    *plugintoken.Signer // verifies plugin service tokens + user assertions (H3)
	dialect   Dialect             // SQL dialect the compiler emits for this store
	dual      *dualstore.Store    // when set, reads unify lite∪scale (RULING-MIG6); nil = single store
	// role resolves a session user's membership role in the org that owns a project
	// (Arc O / O2) — the per-request-per-project authority the session (Case 2) auth path
	// intersects. Defaults to controlplane.RoleForProject over the pool; injectable so the
	// seam is testable without a DB.
	role func(ctx context.Context, userID, projectID string) (string, error)
}

// SetRoleResolver overrides the per-project role resolver (tests). Nil is ignored.
func (s *Server) SetRoleResolver(fn func(ctx context.Context, userID, projectID string) (string, error)) {
	if fn != nil {
		s.role = fn
	}
}

// SetDualStore enables permanent dual-read: every read unifies the historical
// (lite) and new (scale) backends at the Query API (RULING-MIG6). Get*/Erase/score-
// ingest route through the dual decorator; the compiled-SQL list/aggregation paths
// compile per-dialect and merge. The write path (pipeline) must use the same dual
// store so a span is immediately readable across the boundary.
func (s *Server) SetDualStore(d *dualstore.Store) { s.dual = d }

// pointReads is the non-compiled-SQL read/erase/score-ingest surface both the
// single store and the dual decorator satisfy.
type pointReads interface {
	GetSpan(ctx context.Context, projectID, id string) (json.RawMessage, error)
	GetScore(ctx context.Context, projectID, id string) (json.RawMessage, error)
	GetTraceSpans(ctx context.Context, projectID, traceID string) ([]json.RawMessage, error)
	EraseSpans(ctx context.Context, projectID, userID, actor string, from, to time.Time) (int, string, error)
	PersistScore(ctx context.Context, ev storage.Event) error
}

// reads returns the effective store for the non-SQL paths: the dual decorator when
// dual-read is enabled (so Get/Erase/score-ingest unify lite∪scale), else the single
// store. This is the convergence seam for those paths (R-MIG4).
func (s *Server) reads() pointReads {
	if s.dual != nil {
		return s.dual
	}
	return s.store
}

func NewServer(store storage.TelemetryStore, pool *pgxpool.Pool, log *slog.Logger, maxWindow time.Duration, reg *metrics.Registry, signer *plugintoken.Signer) *Server {
	return &Server{
		store: store, pool: pool, log: log, maxWindow: maxWindow, metrics: reg, signer: signer, dialect: PostgresDialect,
		role: func(ctx context.Context, userID, projectID string) (string, error) {
			return controlplane.RoleForProject(ctx, pool, userID, projectID)
		},
	}
}

// SetDialect selects the SQL dialect the compiler emits, matching the backing
// store (Postgres for lite, ClickHouse for scale). Defaults to Postgres.
func (s *Server) SetDialect(d Dialect) {
	if d != nil {
		s.dialect = d
	}
}

// Handler returns the routed Query API handler (generated routing).
func (s *Server) Handler() http.Handler { return queryapi.Handler(s) }

var _ queryapi.ServerInterface = (*Server)(nil)

// auth resolves the caller and enforces the computed double-token intersection
// (ADR-0023/R3). The returned Identity.Scopes are the caller's EFFECTIVE canonical
// permissions (perm.*), so downstream payload-scope checks intersect losslessly.
// `op` is the historical coarse operation name at the call site; it is translated
// to the required canonical permission here.
//
// NOTE (inert generated stubs): the generated queryapi.server_gen.go declares
// ServiceTokenScopes/UserAssertionScopes security-context values but leaves them
// empty; they are deliberately IGNORED. The real intersection is computed below
// from the verified token + assertion headers — do not wire the generated
// context values up thinking they are authoritative (they never are).
func (s *Server) auth(r *http.Request, op string) (controlplane.Identity, *CompileError) {
	reqPerm := perm.RequiredPermForOp(op)
	svcTok := r.Header.Get("X-LLMObs-Service-Token")
	assertion := r.Header.Get(pluginproto.IdentityAssertionHeader)

	// Case 1 — a plugin acting on behalf of a user (the double token). Effective
	// access is the intersection of the plugin's grant and the user's grant, on the
	// user's project. Never trust a plugin-supplied identity.
	if svcTok != "" && assertion != "" {
		return s.authPlugin(r, op, reqPerm, svcTok, assertion)
	}

	// Case 1b — a plugin FRONTEND request (J1/G1). A kernel-minted frontend token whose
	// scopes are ALREADY the plugin-grant ∩ user-session ∩ project intersection, used
	// directly. Two guards keep it from being widened: a distinct header, AND a signed
	// PurposeFrontend marker (verified in authFrontend) that ONLY the frontend mint sets
	// — so a proxy/jobs-minted identity assertion (un-intersected full-role scopes)
	// cannot be replayed here for more than it was granted.
	//
	// G1 — FAIL CLOSED on the frontend path. A request is plugin-originated if it
	// carries EITHER the frontend token OR the plugin-frontend marker (the SDK sends the
	// marker on every plugin call). Such a request MUST resolve through the intersected
	// frontend token and MUST NOT fall through to the session-cookie full-scope path
	// (Case 2) below: a marked request with a missing/expired/invalid token is REJECTED,
	// not silently run at the user's full permissions. This is what actually enforces
	// least-privilege for a cooperating frontend — the escalation this fix closes.
	//
	// SECURITY NOTE — least-privilege-by-default, NOT a boundary. A HOSTILE same-origin
	// frontend can still omit BOTH the marker and the token and call with the ambient
	// session cookie (Case 2); containing that is origin isolation, the deferred future
	// boundary (ADR-0004 amendment). This case confines a cooperating SDK-using frontend.
	ft := r.Header.Get(pluginproto.FrontendTokenHeader)
	if svcTok == "" && (ft != "" || r.Header.Get(pluginproto.PluginFrontendHeader) != "") {
		if ft == "" {
			return controlplane.Identity{}, errf("unauthorized", 401, "plugin frontend request requires a frontend token")
		}
		return s.authFrontend(r.Context(), op, reqPerm, ft)
	}

	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

	// Case 2 — a browser session (shell). Effective = the user's role permissions ∩
	// project, where the role is resolved PER-PROJECT-PER-ORG (Arc O / O2): the user's
	// membership role in the org that owns the requested project — NOT the ambient
	// default-org role on the session. A user who is owner in org A and viewer in org B
	// gets viewer scope when acting on org B's project; a non-member (or unknown project)
	// gets role "" → RoleScopes("") = no scopes → 403 (fail closed). This is the cross-org
	// isolation the ambient field masked.
	if bearer == "" && svcTok == "" {
		if sess, ok := authhttp.SessionFrom(r.Context()); ok {
			projectID, err := s.sessionProject(r)
			if err != nil {
				return controlplane.Identity{}, errf("unauthorized", 403, "no project available")
			}
			role, rerr := s.role(r.Context(), sess.User.ID, projectID)
			if rerr != nil {
				return controlplane.Identity{}, errf("unauthorized", 403, "role resolution failed")
			}
			userPerms := perm.RoleScopes(role)
			if !perm.Has(userPerms, reqPerm) {
				return controlplane.Identity{}, errf("unauthorized", 403, "missing permission %s", reqPerm)
			}
			return controlplane.Identity{ProjectID: projectID, Scopes: userPerms}, nil
		}
	}

	// Case 3 — a machine api key. A bare service token with no assertion cannot act
	// (no user) and will fail Authenticate — a plugin MUST forward the assertion.
	if bearer == "" {
		bearer = svcTok
	}
	id, err := controlplane.Authenticate(r.Context(), s.pool, bearer)
	if err != nil {
		return controlplane.Identity{}, errf("unauthorized", 403, "invalid credentials")
	}
	id.Scopes = perm.ExpandCoarse(id.Scopes) // translate coarse key scopes UP to canonical
	if !perm.Has(id.Scopes, reqPerm) {
		return controlplane.Identity{}, errf("unauthorized", 403, "missing permission %s", reqPerm)
	}
	return id, nil
}

// authPlugin computes the double-token intersection for a plugin→kernel call.
func (s *Server) authPlugin(r *http.Request, op, reqPerm, svcTok, assertion string) (controlplane.Identity, *CompileError) {
	if s.signer == nil {
		return controlplane.Identity{}, errf("unauthorized", 403, "plugin auth unavailable")
	}
	ctx := r.Context()
	now := time.Now()
	stc, err := s.signer.VerifyServiceToken(ctx, svcTok, now)
	if err != nil {
		return controlplane.Identity{}, errf("unauthorized", 403, "invalid service token")
	}
	// The assertion MUST have been minted for THIS plugin's audience — an assertion
	// for another plugin is rejected (token-confusion defence).
	ac, err := s.signer.VerifyIdentityAssertion(ctx, assertion, pluginproto.PluginSubject(stc.PluginID), now)
	if err != nil {
		return controlplane.Identity{}, errf("unauthorized", 403, "invalid identity assertion")
	}
	// Capability gate: the plugin must hold the capability for this op (delete has
	// none — plugins may not erase).
	caps, pluginPerms := perm.SplitCapsAndPerms(stc.Scopes)
	if !perm.Has(caps, perm.CapMarker(perm.CapForOp(op))) {
		return controlplane.Identity{}, errf("unauthorized", 403, "plugin lacks capability for %s", op)
	}
	// The computed intersection: effective = plugin data perms ∩ user perms.
	effective := pluginproto.Intersect(pluginPerms, ac.Scopes)
	if !perm.Has(effective, reqPerm) {
		return controlplane.Identity{}, errf("unauthorized", 403, "outside the permission intersection for %s", reqPerm)
	}
	// Project comes from the assertion; the plugin cannot pick a different tenant.
	return controlplane.Identity{ProjectID: ac.ProjectID, Scopes: effective}, nil
}

// authFrontend resolves a plugin frontend-token call (J1). The token is a
// kernel-minted frontend token whose scopes were computed at mint as plugin-grant ∩
// user-session ∩ project, so no further intersection is needed — the token IS the
// intersection. VerifyFrontendToken enforces the signed PurposeFrontend marker, so
// ONLY a real frontend token is accepted: a proxy/jobs-minted identity assertion
// (un-intersected full-role scopes) is rejected here even though it is kernel-signed
// and shares the claim shape. Frontend tokens carry NO capability markers, so they
// can only read/write data within the intersected perms — never reach a
// capability-gated primitive.
func (s *Server) authFrontend(ctx context.Context, op, reqPerm, token string) (controlplane.Identity, *CompileError) {
	if s.signer == nil {
		return controlplane.Identity{}, errf("unauthorized", 403, "plugin auth unavailable")
	}
	ac, err := s.signer.VerifyFrontendToken(ctx, token, time.Now())
	if err != nil {
		return controlplane.Identity{}, errf("unauthorized", 403, "invalid frontend token")
	}
	// Capability gate — mirror authPlugin (server.go authPlugin), enforced at THIS seam
	// (invariant #11). An op with no plugin capability (CapForOp == "") is forbidden to
	// ALL plugins, backend and frontend alike — today GDPR erasure ("delete"). Without
	// this, a plugin whose manifest declared traces:delete would, under an admin session,
	// mint a frontend token carrying that data perm and perform erasure — making the
	// frontend credential strictly MORE powerful than the backend double token, which
	// the capability gate refuses. A frontend token may never authorize a plugin-
	// forbidden primitive, however it was minted.
	if perm.CapForOp(op) == "" {
		return controlplane.Identity{}, errf("unauthorized", 403, "plugin frontends may not perform %q", op)
	}
	if !perm.Has(ac.Scopes, reqPerm) {
		return controlplane.Identity{}, errf("unauthorized", 403, "outside the permission intersection for %s", reqPerm)
	}
	return controlplane.Identity{ProjectID: ac.ProjectID, Scopes: ac.Scopes}, nil
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
		s.runAggregation(w, r, doc, target, id)
		return
	}

	var c *Compiled
	var cerr error
	switch target {
	case "spans":
		c, cerr = CompileSpansForDialect(doc, id.ProjectID, s.maxWindow, s.dialect)
	case "traces":
		c, cerr = CompileTracesForDialect(doc, id.ProjectID, s.maxWindow, s.dialect)
	case "scores":
		c, cerr = CompileScoresForDialect(doc, id.ProjectID, s.maxWindow, s.dialect)
	default:
		writeErr(w, errf("schema_invalid", 400, "target must be one of spans|traces|scores"))
		return
	}
	if cerr != nil {
		writeErr(w, cerr.(*CompileError))
		return
	}

	started := time.Now()
	anchor := "start_time"
	if target == "scores" {
		anchor = "timestamp"
	}
	rows, err := s.listRows(r.Context(), target, doc, id.ProjectID, c)
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

	// Field-level redaction (DSL §10): project payloads out unless the caller holds
	// the payload scope. Cursor keying happened above from promoted anchors, so it
	// never leaks payloads and paging is unaffected.
	strip := payloadFields
	payloadPerm := perm.TracesReadPayloads
	if target == "scores" {
		strip = scorePayloadFields
		payloadPerm = perm.ScoresReadPayloads
	}
	rows = projectRows(rows, strip, id.HasScope(payloadPerm))

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
	if s.metrics != nil {
		lbl := map[string]string{"target": target}
		s.metrics.CounterAdd("llmobs_query_requests_total", "Query requests by target.", lbl, 1)
		s.metrics.Observe("llmobs_query_duration_seconds", "Query latency by target (seconds).", lbl, time.Since(started).Seconds())
	}
	writeJSON(w, http.StatusOK, resp)
}

// runAggregation handles an aggregation query (QD-4). Group rows carry only
// promoted/aggregated values — never payloads — so payload scope does not apply.
func (s *Server) runAggregation(w http.ResponseWriter, r *http.Request, doc map[string]any, target string, id controlplane.Identity) {
	if target != "spans" && target != "traces" && target != "scores" {
		writeErr(w, errf("schema_invalid", 400, "target must be one of spans|traces|scores"))
		return
	}
	ca, cerr := CompileAggregationForDialect(doc, id.ProjectID, s.maxWindow, target, s.dialect)
	if cerr != nil {
		writeErr(w, cerr.(*CompileError))
		return
	}
	started := time.Now()
	groups, warnings, err := s.aggRows(r.Context(), target, doc, id.ProjectID, ca)
	if err != nil {
		s.log.Error("aggregation execution", "err", err.Error())
		writeErr(w, errf("internal", 500, "aggregation failed"))
		return
	}
	if groups == nil {
		groups = []map[string]any{}
	}
	if s.metrics != nil {
		s.metrics.CounterAdd("llmobs_query_requests_total", "Query requests by target.", map[string]string{"target": target + "/agg"}, 1)
		s.metrics.Observe("llmobs_query_duration_seconds", "Query latency by target (seconds).", map[string]string{"target": target + "/agg"}, time.Since(started).Seconds())
	}
	data := make([]json.RawMessage, len(groups))
	for i, g := range groups {
		b, _ := json.Marshal(g)
		data[i] = b
	}
	warns := make([]any, len(warnings))
	for i, w := range warnings {
		warns[i] = w
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  "v1alpha1",
		"data":     data,
		"stats":    map[string]any{"elapsed_ms": time.Since(started).Milliseconds(), "returned": len(groups)},
		"warnings": warns,
	})
}

// Whoami: GET /v1alpha1/whoami — returns the authenticated caller's project and
// effective scopes. Lets a machine client (e.g. the MCP server) verify its key is
// metadata-scoped before starting, and is generally useful for tooling.
func (s *Server) Whoami(w http.ResponseWriter, r *http.Request) {
	id, aerr := s.auth(r, "query")
	if aerr != nil {
		writeErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id":    id.ProjectID,
		"scopes":        id.Scopes,
		"read_payloads": id.HasScope(perm.TracesReadPayloads),
	})
}

// GetSpan: GET /v1alpha1/spans/{id}
func (s *Server) GetSpan(w http.ResponseWriter, r *http.Request, id string) {
	ident, aerr := s.auth(r, "query")
	if aerr != nil {
		writeErr(w, aerr)
		return
	}
	doc, err := s.reads().GetSpan(r.Context(), ident.ProjectID, id)
	if err != nil {
		writeErr(w, errf("internal", 500, "fetch failed"))
		return
	}
	if doc == nil {
		writeErr(w, errf("not_found", 404, "no such span"))
		return
	}
	if !ident.HasScope(perm.TracesReadPayloads) {
		doc = stripPayloadFields(doc, payloadFields)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc)
}

// EraseSpans: DELETE /v1alpha1/spans — GDPR erasure (requires the `delete`
// scope). Bounded, synchronous, audited.
func (s *Server) EraseSpans(w http.ResponseWriter, r *http.Request, params queryapi.EraseSpansParams) {
	ident, aerr := s.auth(r, "delete")
	if aerr != nil {
		writeErr(w, aerr)
		return
	}
	if params.UserId == "" {
		writeErr(w, errf("schema_invalid", 400, "user_id is required"))
		return
	}
	if !params.From.Before(params.To) {
		writeErr(w, errf("schema_invalid", 400, "from must be before to"))
		return
	}
	actor := "apikey"
	if sess, ok := authhttp.SessionFrom(r.Context()); ok {
		actor = "session:" + sess.User.Email
	}
	erased, auditID, err := s.reads().EraseSpans(r.Context(), ident.ProjectID, params.UserId, actor, params.From, params.To)
	if err != nil {
		s.log.Error("erase spans", "err", err.Error())
		writeErr(w, errf("internal", 500, "erasure failed"))
		return
	}
	s.log.Warn("erasure executed", "actor", actor, "user_id", params.UserId, "erased", erased, "audit_id", auditID)
	writeJSON(w, http.StatusOK, map[string]any{"erased": erased, "audit_id": auditID})
}

// GetScore: GET /v1alpha1/scores/{id}.
func (s *Server) GetScore(w http.ResponseWriter, r *http.Request, id string) {
	ident, aerr := s.auth(r, "query")
	if aerr != nil {
		writeErr(w, aerr)
		return
	}
	doc, err := s.reads().GetScore(r.Context(), ident.ProjectID, id)
	if err != nil {
		writeErr(w, errf("internal", 500, "fetch failed"))
		return
	}
	if doc == nil {
		writeErr(w, errf("not_found", 404, "no such score"))
		return
	}
	if !ident.HasScope(perm.ScoresReadPayloads) {
		doc = stripPayloadFields(doc, scorePayloadFields)
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
		if err := s.reads().PersistScore(r.Context(), scoreEvent(sc)); err != nil {
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
