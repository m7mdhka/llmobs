package authhttp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/secretbox"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

// mockIdP is a minimal OIDC provider: discovery + JWKS + token endpoint. The ID token it
// returns is set per-test so we can forge iss/aud/exp/nonce and prove each is rejected.
type mockIdP struct {
	srv     *httptest.Server
	key     *rsa.PrivateKey
	kid     string
	idToken string // the raw signed ID token /token returns next
}

func newMockIdP(t *testing.T) *mockIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	m := &mockIdP{key: key, kid: "test-key-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := m.srv.URL
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                base,
			"authorization_endpoint":                base + "/authorize",
			"token_endpoint":                        base + "/token",
			"jwks_uri":                              base + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{
			{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": m.kid, "n": n, "e": e},
		}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer", "id_token": m.idToken,
		})
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

// sign builds a signed RS256 JWT with the given claims (the test forges iss/aud/exp/nonce).
func (m *mockIdP) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	hb, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": m.kid})
	pb, _ := json.Marshal(claims)
	input := b64(hb) + "." + b64(pb)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, m.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + b64(sig)
}

func ssoFlowSetup(t *testing.T) (*http.ServeMux, *pgxpool.Pool, *mockIdP, string) {
	t.Helper()
	dburl := os.Getenv("LLMOBS_TEST_DATABASE_URL")
	if dburl == "" {
		t.Skip("set LLMOBS_TEST_DATABASE_URL to run the O5 OIDC flow test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dburl)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, s := range []string{
		`DELETE FROM sso_group_roles WHERE org_id='org_o5flow'`,
		`DELETE FROM sso_providers WHERE org_id='org_o5flow'`,
		`DELETE FROM org_memberships WHERE org_id='org_o5flow' OR user_id LIKE 'usr_o5flow%'`,
		`DELETE FROM sessions WHERE user_id LIKE 'usr_o5flow%'`,
		`DELETE FROM users WHERE lower(email) LIKE 'o5flow-%'`,
		`DELETE FROM organizations WHERE id='org_o5flow'`,
	} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("clean: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id,name) VALUES ('org_o5flow','flow')`); err != nil {
		t.Fatal(err)
	}
	idp := newMockIdP(t)
	box, _ := secretbox.NewRandom()
	if err := controlplane.SetSSOProvider(ctx, pool, box, &controlplane.SSOProvider{
		OrgID: "org_o5flow", Issuer: idp.srv.URL, ClientID: "test-client", ClientSecret: "s",
		GroupClaim: "groups", Enabled: true,
		GroupRoles: map[string]string{"admins": perm.RoleAdmin, "eng": perm.RoleMember},
	}); err != nil {
		t.Fatal(err)
	}
	h := New(pool, nil, false)
	mux := http.NewServeMux()
	s := h.RegisterSSO(mux, box, idp.srv.Client(), "http://app.test")
	// The test org IS the login org (in a shared DB the real DefaultOrgID is nondeterministic).
	s.loginOrg = func(context.Context) (string, error) { return "org_o5flow", nil }
	return mux, pool, idp, "org_o5flow"
}

// start drives /start and returns the state cookie + the state/nonce the handler minted.
func ssoStart(t *testing.T, mux *http.ServeMux, org string) (*http.Cookie, string, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/sso/"+org+"/start", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("start: want 302, got %d %s", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == ssoStateCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("start did not set the SSO state cookie")
	}
	return cookie, loc.Query().Get("state"), loc.Query().Get("nonce")
}

func ssoCallback(mux *http.ServeMux, org string, cookie *http.Cookie, state string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/auth/sso/"+org+"/callback?code=abc&state="+state, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestSSOFlowHappyPath: a fully-valid ID token (correct sig/iss/aud/exp/nonce) with a mapped
// group provisions the user and mints a session.
func TestSSOFlowHappyPath(t *testing.T) {
	mux, pool, idp, org := ssoFlowSetup(t)
	cookie, state, nonce := ssoStart(t, mux, org)
	idp.idToken = idp.sign(t, map[string]any{
		"iss": idp.srv.URL, "aud": "test-client", "sub": "abc",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"nonce": nonce, "email": "o5flow-alice@t", "email_verified": true,
		"groups": []string{"admins"},
	})
	rec := ssoCallback(mux, org, cookie, state)
	if rec.Code != http.StatusFound {
		t.Fatalf("valid SSO login must redirect (302), got %d %s", rec.Code, rec.Body.String())
	}
	// A session cookie is set, and the user was JIT-provisioned as admin (the mapped role).
	var gotSession bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			gotSession = true
		}
	}
	if !gotSession {
		t.Fatal("valid SSO login must set a session cookie")
	}
	var uid, role string
	if err := pool.QueryRow(context.Background(),
		`SELECT m.user_id, m.role FROM org_memberships m JOIN users u ON u.id=m.user_id
		  WHERE lower(u.email)='o5flow-alice@t' AND m.org_id=$1`, org).Scan(&uid, &role); err != nil {
		t.Fatalf("JIT user not provisioned: %v", err)
	}
	if role != perm.RoleAdmin {
		t.Fatalf("SSO user role = %q, want admin (the verified group mapping)", role)
	}
	// The account is passwordless (SSO-only).
	var hash *string
	_ = pool.QueryRow(context.Background(), `SELECT password_hash FROM users WHERE id=$1`, uid).Scan(&hash)
	if hash != nil {
		t.Fatal("SSO-provisioned user must have no local password")
	}
}

// TestSSOFlowIdentityOwnership is the boundary-review CRITICAL fix: SSO must NOT authenticate an
// email that owns a LOCAL-password account (an org-controlled IdP could otherwise claim a local
// account), and it must refuse a login into a non-login org (whose role a session wouldn't
// resolve). It also proves the string-typed email_verified:"false" bypass is closed.
func TestSSOFlowIdentityOwnership(t *testing.T) {
	mux, pool, idp, org := ssoFlowSetup(t)
	ctx := context.Background()
	// A pre-existing LOCAL-password account with a mapped email.
	if _, err := pool.Exec(ctx, `INSERT INTO users (id,email,password_hash,role) VALUES ('usr_o5flow_local','o5flow-local@t','x','viewer')`); err != nil {
		t.Fatal(err)
	}

	signValid := func(nonce, email string, ev any) string {
		c := map[string]any{"iss": idp.srv.URL, "aud": "test-client", "sub": "s",
			"exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce, "email": email, "groups": []string{"admins"}}
		if ev != nil {
			c["email_verified"] = ev
		}
		return idp.sign(t, c)
	}

	// 1) An email that owns a LOCAL account → refused (403), no session, account untouched.
	cookie, state, nonce := ssoStart(t, mux, org)
	idp.idToken = signValid(nonce, "o5flow-local@t", true)
	if rec := ssoCallback(mux, org, cookie, state); rec.Code != http.StatusForbidden {
		t.Fatalf("SSO into a local-password account must be 403, got %d %s", rec.Code, rec.Body.String())
	}
	var hash *string
	_ = pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id='usr_o5flow_local'`).Scan(&hash)
	if hash == nil {
		t.Fatal("the local account's password must be untouched by a refused SSO login")
	}

	// 2) email_verified as the STRING "false" must be rejected (the type-bypass fix).
	cookie, state, nonce = ssoStart(t, mux, org)
	idp.idToken = signValid(nonce, "o5flow-str@t", "false")
	if rec := ssoCallback(mux, org, cookie, state); rec.Code != http.StatusUnauthorized {
		t.Fatalf(`email_verified:"false" (string) must be rejected, got %d`, rec.Code)
	}

	// 3) A login into a NON-login org is refused before touching the IdP.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/sso/org_not_login/start", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("SSO into a non-login org must be 404, got %d", rec.Code)
	}
}

// TestSSOFlowRejections is the external-claim trust-boundary matrix: every malformed or forged
// assertion is rejected BEFORE any identity is trusted (no session, no provisioning).
func TestSSOFlowRejections(t *testing.T) {
	mux, pool, idp, org := ssoFlowSetup(t)
	base := func() (*http.Cookie, string, string) { return ssoStart(t, mux, org) }

	tests := []struct {
		name   string
		claims func(nonce string) map[string]any
		mutate func(cookie *http.Cookie, state *string) // tamper with state/cookie
	}{
		{"wrong audience", func(n string) map[string]any {
			return map[string]any{"iss": idp.srv.URL, "aud": "someone-else", "sub": "a", "exp": time.Now().Add(time.Hour).Unix(), "nonce": n, "email": "o5flow-x@t", "groups": []string{"admins"}}
		}, nil},
		{"wrong issuer", func(n string) map[string]any {
			return map[string]any{"iss": "https://evil.example.com", "aud": "test-client", "sub": "a", "exp": time.Now().Add(time.Hour).Unix(), "nonce": n, "email": "o5flow-x@t", "groups": []string{"admins"}}
		}, nil},
		{"expired", func(n string) map[string]any {
			return map[string]any{"iss": idp.srv.URL, "aud": "test-client", "sub": "a", "exp": time.Now().Add(-time.Hour).Unix(), "iat": time.Now().Add(-2 * time.Hour).Unix(), "nonce": n, "email": "o5flow-x@t", "groups": []string{"admins"}}
		}, nil},
		{"nonce mismatch (replay)", func(n string) map[string]any {
			return map[string]any{"iss": idp.srv.URL, "aud": "test-client", "sub": "a", "exp": time.Now().Add(time.Hour).Unix(), "nonce": "different-nonce", "email": "o5flow-x@t", "groups": []string{"admins"}}
		}, nil},
		{"no mapped group", func(n string) map[string]any {
			return map[string]any{"iss": idp.srv.URL, "aud": "test-client", "sub": "a", "exp": time.Now().Add(time.Hour).Unix(), "nonce": n, "email": "o5flow-x@t", "email_verified": true, "groups": []string{"random-group"}}
		}, nil},
		{"state mismatch (CSRF)", func(n string) map[string]any {
			return map[string]any{"iss": idp.srv.URL, "aud": "test-client", "sub": "a", "exp": time.Now().Add(time.Hour).Unix(), "nonce": n, "email": "o5flow-x@t", "groups": []string{"admins"}}
		}, func(_ *http.Cookie, state *string) { *state = "forged-state" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cookie, state, nonce := base()
			idp.idToken = idp.sign(t, tc.claims(nonce))
			if tc.mutate != nil {
				tc.mutate(cookie, &state)
			}
			rec := ssoCallback(mux, org, cookie, state)
			if rec.Code == http.StatusFound {
				t.Fatalf("%s: a rejected assertion must NOT mint a session, got 302", tc.name)
			}
			if rec.Code < 400 {
				t.Fatalf("%s: want a 4xx rejection, got %d %s", tc.name, rec.Code, rec.Body.String())
			}
			// No session cookie may be set on a rejection.
			for _, c := range rec.Result().Cookies() {
				if c.Name == sessionCookie && c.Value != "" {
					t.Fatalf("%s: a rejected assertion set a session cookie", tc.name)
				}
			}
		})
	}
	// Nothing above should have provisioned the forged identity.
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM users WHERE lower(email)='o5flow-x@t'`).Scan(&n)
	if n != 0 {
		t.Fatalf("a rejected assertion must never provision a user, found %d", n)
	}
}
