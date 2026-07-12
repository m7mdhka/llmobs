package clickhouse

import (
	"context"
	"embed"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// clusterNameRE validates an operator-supplied cluster name before it is
// interpolated into DDL. ClickHouse cluster names are simple identifiers; we
// refuse anything else so a config value can never smuggle SQL into an
// `ON CLUSTER` clause — fail early and name the problem rather than let a config
// value smuggle SQL into replicated DDL.
var clusterNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// zkPath is the ReplicatedMergeTree keeper path. The {uuid} macro binds the
// path to the table's own UUID (Atomic database), so ON CLUSTER DDL needs no
// per-table path and re-running is a no-op; {shard}/{replica} are the standard
// server macros. Operators who diverge override via a keeper macro, not here.
const zkPath = "/clickhouse/tables/{uuid}/{shard}"

// conn is the narrow ClickHouse surface the migrator needs, declared at the
// consumer per go-style. clickhouse-go's driver.Conn satisfies it.
type conn interface {
	Exec(ctx context.Context, query string, args ...any) error
	Query(ctx context.Context, query string, args ...any) (driver.Rows, error)
}

// Config carries the migration-relevant deployment shape.
type Config struct {
	// Cluster is the ClickHouse cluster name for ON CLUSTER DDL. Empty means
	// standalone / Replicated-database mode: no ON CLUSTER, plain MergeTree-family
	// engines (the Replicated database, if any, replicates DDL itself). Non-empty
	// selects Atomic + ON CLUSTER + Replicated* engines. One migration set stays
	// deterministic under both topologies.
	Cluster string
}

func (c Config) validate() error {
	if c.Cluster != "" && !clusterNameRE.MatchString(c.Cluster) {
		return fmt.Errorf("clickhouse cluster name %q is not a valid identifier ([A-Za-z0-9_-]+)", c.Cluster)
	}
	if c.Cluster == "default" {
		// A `default` cluster name is almost always
		// an un-overridden template, and applying replicated DDL to the wrong cluster
		// is destructive. Refuse it explicitly rather than silently proceed.
		return fmt.Errorf("clickhouse cluster name %q is refused: set the real cluster name (R-CH1, no default literal)", c.Cluster)
	}
	return nil
}

// substitutions returns the DDL placeholder → concrete-SQL map for the topology.
func (c Config) substitutions() map[string]string {
	if c.Cluster == "" {
		return map[string]string{
			"{{on_cluster}}":                "",
			"{{engine_replacing_ver}}":      "ReplacingMergeTree(ver)",
			"{{engine_replacing_event_ts}}": "ReplacingMergeTree(event_ts)",
			"{{engine_mergetree}}":          "MergeTree()",
		}
	}
	return map[string]string{
		"{{on_cluster}}":                fmt.Sprintf("ON CLUSTER `%s`", c.Cluster),
		"{{engine_replacing_ver}}":      fmt.Sprintf("ReplicatedReplacingMergeTree('%s', '{replica}', ver)", zkPath),
		"{{engine_replacing_event_ts}}": fmt.Sprintf("ReplicatedReplacingMergeTree('%s', '{replica}', event_ts)", zkPath),
		"{{engine_mergetree}}":          fmt.Sprintf("ReplicatedMergeTree('%s', '{replica}')", zkPath),
	}
}

func (c Config) render(sql string) string {
	out := sql
	for placeholder, concrete := range c.substitutions() {
		out = strings.ReplaceAll(out, placeholder, concrete)
	}
	return out
}

// Migrate applies pending embedded migrations. ClickHouse has no multi-statement
// transactions, so each statement is applied on its own; every DDL is IF NOT
// EXISTS so a re-run, or a concurrent apply from another replica, is a
// no-op. Migration state lives in schema_migrations, tracked replicated-safely
// via the same engine templating as the rest of the schema.
func Migrate(ctx context.Context, db conn, cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}

	// schema_migrations bookkeeping — created with the topology's engine so it
	// replicates alongside the data tables.
	ddl := cfg.render(`CREATE TABLE IF NOT EXISTS schema_migrations {{on_cluster}} (
		version    String,
		applied_at DateTime64(6) DEFAULT now64(6)
	) ENGINE = {{engine_mergetree}}
	ORDER BY version`)
	if err := db.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := db.Query(ctx, "SELECT DISTINCT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close schema_migrations rows: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, f := range files {
		version := strings.TrimSuffix(f, ".sql")
		if applied[version] {
			continue
		}
		raw, err := migrationsFS.ReadFile("migrations/" + f)
		if err != nil {
			return err
		}
		for _, stmt := range splitStatements(cfg.render(string(raw))) {
			if err := db.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("apply %s: %w", f, err)
			}
		}
		if err := db.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", version); err != nil {
			return fmt.Errorf("record %s: %w", f, err)
		}
	}
	return nil
}

// splitStatements strips `--` line comments (preserving newlines), then breaks
// the file into statements on the semicolon terminator. Comments MUST be removed
// before the split: a `;` inside a comment (e.g. an inline example) would
// otherwise sever a statement mid-DDL. Our executable DDL contains no semicolons
// inside string literals, so the post-strip split is safe. Blank chunks drop.
func splitStatements(sql string) []string {
	code := stripLineComments(sql)
	var out []string
	for _, chunk := range strings.Split(code, ";") {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		out = append(out, strings.TrimSpace(chunk))
	}
	return out
}

// stripLineComments removes `--` line comments while preserving line structure,
// so the executable SQL keeps its token spacing (blank comment lines become empty
// lines, which ClickHouse ignores).
func stripLineComments(sql string) string {
	lines := strings.Split(sql, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}
