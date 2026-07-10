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
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/authhttp"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/queryapi"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/metrics"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
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
}

func NewServer(store storage.TelemetryStore, pool *pgxpool.Pool, log *slog.Logger, maxWindow time.Duration, reg *metrics.Registry, signer *plugintoken.Signer) *Server {
	return &Server{store: store, pool: pool, log: log, maxWindow: maxWindow, metrics: reg, signer: signer}
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

	// Case 1b — a plugin FRONTEND token (J1). A kernel-minted identity assertion the
	// shell handed the plugin's frontend; its scopes are ALREADY the plugin-grant ∩
	// user-session ∩ project intersection, so they are used directly. Distinct header
	// from the backend assertion path so a bare user-scoped assertion can never be
	// smuggled in here for full scopes.
	//
	// SECURITY NOTE — least-privilege-by-default, NOT a boundary. A plugin frontend
	// runs in the shell's origin (ADR-0004) and can bypass this token by calling with
	// the ambient session cookie (Case 2) directly. This case CONFINES a cooperating
	// SDK-using frontend; it does not contain a hostile one. Origin isolation is the
	// future boundary (ADR-0004 amendment). See frontendtoken.Handler.
	if ft := r.Header.Get("X-LLMObs-Frontend-Token"); ft != "" && svcTok == "" {
		return s.authFrontend(op, reqPerm, ft)
	}

	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

	// Case 2 — a browser session (shell). Effective = the user's role permissions ∩
	// project. Admin => the full permission set, DERIVED from the role (the #21 RBAC
	// seam), not a hardcoded verb list.
	if bearer == "" && svcTok == "" {
		if sess, ok := authhttp.SessionFrom(r.Context()); ok {
			projectID, err := s.sessionProject(r)
			if err != nil {
				return controlplane.Identity{}, errf("unauthorized", 403, "no project available")
			}
			userPerms := perm.RoleScopes(sess.User.Role)
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
func (s *Server) authPlugin(_ *http.Request, op, reqPerm, svcTok, assertion string) (controlplane.Identity, *CompileError) {
	if s.signer == nil {
		return controlplane.Identity{}, errf("unauthorized", 403, "plugin auth unavailable")
	}
	now := time.Now()
	stc, err := s.signer.VerifyServiceToken(svcTok, now)
	if err != nil {
		return controlplane.Identity{}, errf("unauthorized", 403, "invalid service token")
	}
	// The assertion MUST have been minted for THIS plugin's audience — an assertion
	// for another plugin is rejected (token-confusion defence).
	ac, err := s.signer.VerifyIdentityAssertion(assertion, pluginproto.PluginSubject(stc.PluginID), now)
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
// kernel-minted identity assertion whose scopes were computed at mint as
// plugin-grant ∩ user-session ∩ project, so no further intersection is needed —
// the token IS the intersection. We verify it is kernel-signed, unexpired, and
// audience-bound to a plugin (expectedAud="" accepts any plugin audience: there is
// no service token here to name a specific plugin, and the scopes already bound the
// access). Frontend tokens carry NO capability markers, so they can only read/write
// data within the intersected perms — never reach a capability-gated primitive.
func (s *Server) authFrontend(op, reqPerm, token string) (controlplane.Identity, *CompileError) {
	if s.signer == nil {
		return controlplane.Identity{}, errf("unauthorized", 403, "plugin auth unavailable")
	}
	ac, err := s.signer.VerifyIdentityAssertion(token, "", time.Now())
	if err != nil {
		return controlplane.Identity{}, errf("unauthorized", 403, "invalid frontend token")
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
	ca, cerr := CompileAggregation(doc, id.ProjectID, s.maxWindow, target)
	if cerr != nil {
		writeErr(w, cerr.(*CompileError))
		return
	}
	started := time.Now()
	groups, err := s.store.QueryAggregation(r.Context(), target, ca.Select, ca.Where, ca.GroupBy, ca.Args)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  "v1alpha1",
		"data":     data,
		"stats":    map[string]any{"elapsed_ms": time.Since(started).Milliseconds(), "returned": len(groups)},
		"warnings": []any{},
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
	doc, err := s.store.GetSpan(r.Context(), ident.ProjectID, id)
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
	erased, auditID, err := s.store.EraseSpans(r.Context(), ident.ProjectID, params.UserId, actor, params.From, params.To)
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
	doc, err := s.store.GetScore(r.Context(), ident.ProjectID, id)
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
