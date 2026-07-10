// Package authhttp is the HTTP edge of local single-admin auth (PR-D1): login /
// logout / me endpoints, a server-side session cookie, CSRF enforcement on
// state-changing routes, and the session middleware that terminates auth for the
// rest of the gateway. It is deliberately the single place that turns a cookie
// into an identity — the forward-looking site where kernel-signed identity
// assertions will be minted for plugin calls (see mintIdentityAssertion).
package authhttp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/pkg/brand"
)

const (
	sessionCookie = "llmobs_session"
	csrfHeader    = "X-CSRF-Token"
)

type ctxKey int

const sessionKey ctxKey = 0

// Handler serves the auth endpoints and provides the session middleware.
type Handler struct {
	pool     *pgxpool.Pool
	log      *slog.Logger
	limiter  *rateLimiter
	secure   bool // set Secure on cookies (behind TLS/where the deployment is https)
	cookieNS string
}

// New builds the auth handler. secure controls the cookie Secure attribute.
func New(pool *pgxpool.Pool, log *slog.Logger, secure bool) *Handler {
	return &Handler{
		pool:     pool,
		log:      log,
		limiter:  newRateLimiter(10, time.Minute), // 10 login attempts / IP / minute
		secure:   secure,
		cookieNS: strings.ToLower(brand.Name),
	}
}

// Register mounts the auth routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/auth/login", h.login)
	mux.HandleFunc("/auth/logout", h.logout)
	mux.HandleFunc("/auth/me", h.me)
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userResp struct {
	User struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Role  string `json:"role"`
	} `json:"user"`
	CSRFToken string `json:"csrf_token"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	ip := clientIP(r)
	if !h.limiter.allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts")
		return
	}
	var req loginReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "schema_invalid", "invalid JSON")
		return
	}
	u, err := controlplane.VerifyPassword(r.Context(), h.pool, req.Email, req.Password)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	sess, err := controlplane.CreateSession(r.Context(), h.pool, u)
	if err != nil {
		h.log.Error("create session", "err", err.Error())
		writeErr(w, http.StatusInternalServerError, "internal", "could not create session")
		return
	}
	h.setSessionCookie(w, sess.Token, sess.ExpiresAt)
	writeJSON(w, http.StatusOK, buildUserResp(u, sess.CSRFToken))
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	sess, ok := SessionFrom(r.Context())
	if !ok {
		// Already unauthenticated: clear any stale cookie and succeed idempotently.
		h.clearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !h.checkCSRF(r, sess) {
		writeErr(w, http.StatusForbidden, "csrf_failed", "missing or invalid CSRF token")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = controlplane.DeleteSession(r.Context(), h.pool, c.Value)
	}
	h.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	sess, ok := SessionFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "no active session")
		return
	}
	writeJSON(w, http.StatusOK, buildUserResp(sess.User, sess.CSRFToken))
}

// Middleware resolves the session cookie and injects the session into the request
// context (non-rejecting: downstream handlers decide what requires auth). This is
// where auth is terminated for the whole gateway.
func (h *Handler) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil {
			if sess, serr := controlplane.ResolveSession(r.Context(), h.pool, c.Value); serr == nil {
				// Forward-looking seam: this is where a per-request kernel-signed
				// identity assertion will be minted for plugin→kernel calls.
				// mintIdentityAssertion(sess) — not built in D1.
				r = r.WithContext(context.WithValue(r.Context(), sessionKey, sess))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAuth wraps a handler so it 401s without a valid session, and enforces
// CSRF on state-changing methods.
func (h *Handler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := SessionFrom(r.Context())
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if isStateChanging(r.Method) && !h.checkCSRF(r, sess) {
			writeErr(w, http.StatusForbidden, "csrf_failed", "missing or invalid CSRF token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SessionFrom returns the session bound to the context, if any.
func SessionFrom(ctx context.Context) (controlplane.Session, bool) {
	s, ok := ctx.Value(sessionKey).(controlplane.Session)
	return s, ok
}

func (h *Handler) checkCSRF(r *http.Request, sess controlplane.Session) bool {
	got := r.Header.Get(csrfHeader)
	return got != "" && subtleEqual(got, sess.CSRFToken)
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func buildUserResp(u controlplane.User, csrf string) userResp {
	var resp userResp
	resp.User.ID = u.ID
	resp.User.Email = u.Email
	resp.User.Role = u.Role
	resp.CSRFToken = csrf
	return resp
}

func isStateChanging(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}
