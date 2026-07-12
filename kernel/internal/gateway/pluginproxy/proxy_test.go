package pluginproxy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/authhttp"
	"github.com/m7mdhka/llmobs/kernel/internal/plugintest"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

type fakeBackends struct {
	url     string
	running bool
}

func (f fakeBackends) BackendFor(string) (string, bool) { return f.url, f.running }

// withSession injects a resolved admin session into the request context, as the
// gateway auth middleware would.
func withSession(r *http.Request) *http.Request {
	var sess controlplane.Session
	sess.User.ID = "admin"
	sess.User.Email = "admin@example.com"
	sess.User.Role = "admin"
	return r.WithContext(authhttp.WithSession(r.Context(), sess))
}

func newProxy(url string, running bool) (*Proxy, *plugintoken.Signer) {
	signer, _ := plugintoken.NewSigner()
	p := New(fakeBackends{url: url, running: running}, signer,
		func(*http.Request) (string, error) { return "proj_default", nil },
		func(_ context.Context, userID, _ string) (string, error) { return userID, nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return p, signer
}

// TestProxyStripsCookieInjectsAssertion proves: the plugin never
// sees the session cookie, and it receives a verifiable, audience-bound identity
// assertion for the acting user.
func TestProxyStripsCookieInjectsAssertion(t *testing.T) {
	fake := plugintest.NewFakeBackend(pluginproto.Info{ID: "acme/widget"})
	defer fake.Close()
	p, signer := newProxy(fake.URL(), true)

	r := httptest.NewRequest(http.MethodGet, "/api/plugins/acme/widget/echo", nil)
	r.Header.Set("Cookie", "llmobs_session=secret")
	r = withSession(r)
	rec := httptest.NewRecorder()
	p.Handler("/api/plugins/").ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("proxy should reach the backend, got %d: %s", rec.Code, rec.Body.String())
	}
	var echo struct {
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &echo); err != nil {
		t.Fatal(err)
	}
	if echo.Path != "/echo" {
		t.Fatalf("path not forwarded, got %q", echo.Path)
	}
	if echo.Headers[http.CanonicalHeaderKey("Cookie")] != "" {
		t.Fatalf("cookie must be stripped, got %q", echo.Headers["Cookie"])
	}
	asr := echo.Headers[http.CanonicalHeaderKey(pluginproto.IdentityAssertionHeader)]
	if asr == "" {
		t.Fatalf("identity assertion must be injected; headers=%v", echo.Headers)
	}
	// The plugin can verify it, bound to its own audience.
	claims, err := signer.VerifyIdentityAssertion(context.Background(), asr, pluginproto.PluginSubject("acme/widget"), time.Now())
	if err != nil {
		t.Fatalf("plugin should verify its assertion: %v", err)
	}
	if claims.ProjectID != "proj_default" || claims.Sub != "admin@example.com" {
		t.Fatalf("assertion claims wrong: %+v", claims)
	}
}

func TestProxyUnavailableWhenNotRunning(t *testing.T) {
	p, _ := newProxy("http://127.0.0.1:1", false) // not running
	r := withSession(httptest.NewRequest(http.MethodGet, "/api/plugins/acme/widget/x", nil))
	rec := httptest.NewRecorder()
	p.Handler("/api/plugins/").ServeHTTP(rec, r)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("degraded plugin should be 503, got %d", rec.Code)
	}
}

func TestProxyRequiresSession(t *testing.T) {
	p, _ := newProxy("http://127.0.0.1:1", true)
	r := httptest.NewRequest(http.MethodGet, "/api/plugins/acme/widget/x", nil) // no session
	rec := httptest.NewRecorder()
	p.Handler("/api/plugins/").ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated proxy call should be 401, got %d", rec.Code)
	}
}

func TestProxyUnknownPlugin(t *testing.T) {
	signer, _ := plugintoken.NewSigner()
	p := New(fakeBackends{url: "", running: false}, signer,
		func(*http.Request) (string, error) { return "proj_default", nil },
		func(_ context.Context, userID, _ string) (string, error) { return userID, nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	r := withSession(httptest.NewRequest(http.MethodGet, "/api/plugins/acme/ghost/x", nil))
	rec := httptest.NewRecorder()
	p.Handler("/api/plugins/").ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown plugin should be 404, got %d", rec.Code)
	}
}
