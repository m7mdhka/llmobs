package clickhouse

import (
	"net/url"
	"strings"
	"testing"
)

func TestBuildDSNEncodesCredentials(t *testing.T) {
	// A password with DSN-hostile characters must round-trip losslessly.
	pw := "p@ss:w/rd#1?x"
	dsn := BuildDSN("ch.internal", 9000, "llmobs", "svc user", pw, false)

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("BuildDSN produced an unparseable DSN %q: %v", dsn, err)
	}
	if u.Scheme != "clickhouse" {
		t.Errorf("scheme = %q, want clickhouse", u.Scheme)
	}
	if u.Hostname() != "ch.internal" || u.Port() != "9000" {
		t.Errorf("authority = %q, want ch.internal:9000", u.Host)
	}
	if u.Path != "/llmobs" {
		t.Errorf("path = %q, want /llmobs", u.Path)
	}
	gotPw, _ := u.User.Password()
	if gotPw != pw {
		t.Errorf("password round-trip = %q, want %q", gotPw, pw)
	}
	if u.User.Username() != "svc user" {
		t.Errorf("username round-trip = %q, want %q", u.User.Username(), "svc user")
	}
	// The raw DSN must not contain the unescaped hostile chars in the authority
	// (they'd corrupt parsing); they're percent-encoded.
	if strings.Contains(dsn, "p@ss:w/rd#1") {
		t.Errorf("credentials not encoded in DSN: %q", dsn)
	}
}

func TestBuildDSNSecure(t *testing.T) {
	dsn := BuildDSN("h", 9440, "db", "u", "p", true)
	if !strings.Contains(dsn, "secure=true") {
		t.Errorf("secure DSN missing secure=true: %q", dsn)
	}
}

func TestRequiredGrantsStable(t *testing.T) {
	g := RequiredGrants()
	if len(g) == 0 {
		t.Fatal("RequiredGrants must not be empty")
	}
	// Returned slice is a copy — mutating it must not affect the source.
	g[0] = "MUTATED"
	if RequiredGrants()[0] == "MUTATED" {
		t.Error("RequiredGrants leaked its backing array")
	}
}
