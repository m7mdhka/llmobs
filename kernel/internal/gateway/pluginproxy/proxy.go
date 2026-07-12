// Package pluginproxy proxies user traffic to a plugin backend under the stable
// gateway prefix /api/plugins/{id}/*. It is the single place a
// browser session becomes a plugin-bound identity: the session cookie is STRIPPED
// and a short-TTL, audience-bound identity assertion is INJECTED, so the plugin
// never sees the cookie and can verify who the user is without trusting itself.
// Only running plugins are proxied; otherwise a typed unavailable-state response
// lets the shell degrade gracefully.
package pluginproxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/authhttp"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// BackendLookup resolves a plugin's backend URL and running state (the supervisor).
type BackendLookup interface {
	BackendFor(id string) (url string, running bool)
}

// Proxy forwards user requests to plugin backends.
type Proxy struct {
	backends BackendLookup
	signer   *plugintoken.Signer
	project  func(*http.Request) (string, error)
	// role resolves the session user's membership role in the org that owns the proxied
	// project — the identity assertion carries the user's role IN THAT PROJECT'S ORG,
	// not an ambient default-org role. Injected (no pool import here).
	role func(ctx context.Context, userID, projectID string) (string, error)
	ttl  time.Duration
	log  *slog.Logger
}

// New builds a proxy. project resolves the tenant the request acts on; role resolves the
// user's per-project-org membership role.
func New(backends BackendLookup, signer *plugintoken.Signer, project func(*http.Request) (string, error), role func(context.Context, string, string) (string, error), log *slog.Logger) *Proxy {
	return &Proxy{backends: backends, signer: signer, project: project, role: role, ttl: 2 * time.Minute, log: log}
}

// Handler proxies {prefix}/{id}/* to the plugin backend.
func (p *Proxy) Handler(prefix string) http.Handler {
	prefix = strings.TrimRight(prefix, "/") + "/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, tail, ok := splitPluginID(strings.TrimPrefix(r.URL.Path, prefix))
		if !ok {
			http.NotFound(w, r)
			return
		}
		sess, ok := authhttp.SessionFrom(r.Context())
		if !ok {
			unavailable(w, http.StatusUnauthorized, "authentication_required", id)
			return
		}
		backendURL, running := p.backends.BackendFor(id)
		if backendURL == "" {
			unavailable(w, http.StatusNotFound, "plugin_not_found", id)
			return
		}
		if !running {
			// Typed unavailable-state: the shell renders a degraded surface.
			unavailable(w, http.StatusServiceUnavailable, "plugin_unavailable", id)
			return
		}
		projectID, err := p.project(r)
		if err != nil {
			unavailable(w, http.StatusForbidden, "no_project", id)
			return
		}
		// The assertion carries the user's role IN THE PROJECT'S ORG, not an ambient
		// default-org role. DataPermsOnly then strips management scopes so a plugin
		// identity assertion can never carry members:manage/org:manage even before the
		// downstream intersection (the assertion is handed to plugin code).
		role, rerr := p.role(r.Context(), sess.User.ID, projectID)
		if rerr != nil {
			unavailable(w, http.StatusForbidden, "role_resolution_failed", id)
			return
		}
		assertion, _, err := p.signer.MintIdentityAssertion(id, sess.User.Email, projectID,
			"session:"+sess.User.Email, perm.DataPermsOnly(perm.RoleScopes(role)), time.Now(), p.ttl)
		if err != nil {
			unavailable(w, http.StatusInternalServerError, "assertion_mint_failed", id)
			return
		}
		target, err := url.Parse(backendURL)
		if err != nil {
			unavailable(w, http.StatusBadGateway, "bad_backend_url", id)
			return
		}
		(&httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.URL.Scheme = target.Scheme
				req.URL.Host = target.Host
				req.URL.Path = singleJoin(target.Path, tail)
				req.Host = target.Host
				// The plugin NEVER sees the session cookie or CSRF token.
				req.Header.Del("Cookie")
				req.Header.Del("X-CSRF-Token")
				// Inject the kernel-signed identity assertion (the user half).
				req.Header.Set(pluginproto.IdentityAssertionHeader, assertion)
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				p.log.Warn("plugin proxy error", "plugin_id", id, "err", err.Error())
				unavailable(w, http.StatusBadGateway, "plugin_unreachable", id)
			},
		}).ServeHTTP(w, r)
	})
}

func splitPluginID(rest string) (id, tail string, ok bool) {
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	id = parts[0] + "/" + parts[1]
	tail = "/"
	if len(parts) == 3 {
		tail = "/" + parts[2]
	}
	return id, tail, true
}

func singleJoin(a, b string) string {
	return strings.TrimRight(a, "/") + "/" + strings.TrimLeft(b, "/")
}

func unavailable(w http.ResponseWriter, code int, reason, id string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": reason, "plugin": id})
}
