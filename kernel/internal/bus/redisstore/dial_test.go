package redisstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestCredentialsProviderRotates proves the refreshing-credential path: the provider
// re-reads the password file on EVERY call (each go-redis (re)connect), so a rotated
// managed-cloud token — e.g. an AWS ElastiCache IAM token or a GCP Memorystore rotating
// AUTH — is honored WITHOUT a kernel restart. It also trims the whitespace/newline a
// sidecar commonly writes, and passes the ACL/IAM username through.
func TestCredentialsProviderRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("token-v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Username: "iam-user", PasswordFile: path}
	prov := cfg.credentialsProvider()
	if prov == nil {
		t.Fatal("a PasswordFile must yield a credentials provider")
	}

	user, pass, err := prov(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if user != "iam-user" {
		t.Fatalf("username = %q, want iam-user", user)
	}
	if pass != "token-v1" { // trailing newline trimmed
		t.Fatalf("password = %q, want token-v1 (trimmed)", pass)
	}

	// Rotate the file — the NEXT connect must pick up the new token.
	if err := os.WriteFile(path, []byte("  token-v2  "), 0o600); err != nil {
		t.Fatal(err)
	}
	_, pass2, err := prov(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pass2 != "token-v2" {
		t.Fatalf("after rotation password = %q, want token-v2 — a stale token would force a kernel restart to pick up a rotated credential", pass2)
	}
}

func TestCredentialsProviderNilForStaticPassword(t *testing.T) {
	// The legacy static-password path must be untouched: no PasswordFile → no provider,
	// so go-redis uses the static Username/Password (back-compat).
	if p := (Config{Password: "static"}).credentialsProvider(); p != nil {
		t.Fatal("no PasswordFile must yield a nil provider (static-password path preserved)")
	}
}

func TestCredentialsProviderMissingFileErrors(t *testing.T) {
	prov := Config{PasswordFile: "/no/such/token"}.credentialsProvider()
	if _, _, err := prov(context.Background()); err == nil {
		t.Fatal("a missing password file must surface an error, not an empty credential")
	}
}

// TestCredentialsProviderEmptyFileErrors covers the mid-rotation truncation race: an
// empty/whitespace-only token file must be a (transient) error, never a silent empty
// AUTH credential.
func TestCredentialsProviderEmptyFileErrors(t *testing.T) {
	dir := t.TempDir()
	for _, content := range []string{"", "   \n\t "} {
		path := filepath.Join(dir, "tok")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := (Config{PasswordFile: path}).credentialsProvider()(context.Background()); err == nil {
			t.Fatalf("empty/whitespace token file (%q) must error, not yield an empty credential", content)
		}
	}
}

// TestClassifyAuth is the failure-taxonomy proof: a credential rejection is PERMANENT
// (retrying cannot fix bad creds), while every other error (network, timeout, MOVED) stays
// transient/retriable — so the caller doesn't loop forever hammering a doomed AUTH, nor
// give up on a recoverable blip.
func TestClassifyAuth(t *testing.T) {
	permanent := []error{
		errors.New("WRONGPASS invalid username-password pair or user is disabled"),
		errors.New("NOAUTH Authentication required"),
		fmt.Errorf("wrap: %w", errors.New("invalid username-password")),
	}
	for _, e := range permanent {
		if got := classifyAuth(e); !errors.Is(got, ErrPermanentAuth) {
			t.Fatalf("auth error %v must classify as permanent, got %v", e, got)
		}
	}

	transient := []error{
		errors.New("dial tcp 10.0.0.1:6379: connect: connection refused"),
		errors.New("read tcp: i/o timeout"),
		errors.New("MOVED 3999 127.0.0.1:6381"),
		context.DeadlineExceeded,
	}
	for _, e := range transient {
		if got := classifyAuth(e); errors.Is(got, ErrPermanentAuth) {
			t.Fatalf("transient error %v must NOT classify as permanent (it stays retriable), got %v", e, got)
		}
	}

	if classifyAuth(nil) != nil {
		t.Fatal("classifyAuth(nil) must be nil")
	}
}
