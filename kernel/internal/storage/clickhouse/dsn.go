package clickhouse

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
)

// BuildDSN assembles a clickhouse-go DSN with URL-safe-encoded credentials.
// Passwords routinely contain `@`, `/`, `:`, `#` — pasted verbatim into
// a DSN they corrupt the authority and the connection fails with a misleading
// parse error. Every component is percent-encoded here so any credential is
// carried losslessly. Callers who already hold a fully-formed, correctly-encoded
// DSN (or pass credentials out-of-band) may bypass this.
func BuildDSN(host string, port int, database, user, password string, secure bool) string {
	scheme := "clickhouse"
	u := url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   "/" + database,
	}
	// url.Userinfo percent-encodes the username and password on String().
	u.User = url.UserPassword(user, password)
	if secure {
		q := url.Values{}
		q.Set("secure", "true")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// requiredGrants is the exact privilege set the migration + write path needs.
// Documented here and preflight-checked so a missing grant fails early,
// naming what to grant, instead of surfacing as an opaque mid-migration error.
var requiredGrants = []string{
	"CREATE TABLE",
	"INSERT",
	"SELECT",
	"ALTER",      // migrations that add columns / TTLs
	"OPTIMIZE",   // force-merge in maintenance / tests
	"DROP TABLE", // reversible migrations + test teardown
}

// RequiredGrants returns the privileges an operator must grant the LLMObs
// ClickHouse user, for documentation and the preflight message.
func RequiredGrants() []string {
	out := make([]string, len(requiredGrants))
	copy(out, requiredGrants)
	return out
}

// grantHint formats the operator-facing GRANT statement for the given database.
func grantHint(database string) string {
	g := ""
	for i, priv := range requiredGrants {
		if i > 0 {
			g += ", "
		}
		g += priv
	}
	return fmt.Sprintf("GRANT %s ON %s.* TO <llmobs_user>", g, database)
}
