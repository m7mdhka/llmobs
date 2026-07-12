package clickhouse

import (
	"strings"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cluster string
		wantErr bool
	}{
		{"empty is standalone", "", false},
		{"simple name", "prod_ch", false},
		{"hyphen ok", "ch-east-1", false},
		{"default literal refused", "default", true},
		{"space rejected", "my cluster", true},
		{"injection rejected", "x` ON CLUSTER `y", true},
		{"quote rejected", "a'b", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Config{Cluster: tt.cluster}.validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("validate(%q) err=%v, wantErr=%v", tt.cluster, err, tt.wantErr)
			}
		})
	}
}

func TestRenderStandalone(t *testing.T) {
	out := Config{Cluster: ""}.render(
		"CREATE TABLE x {{on_cluster}} (a String) ENGINE = {{engine_replacing_event_ts}}; " +
			"CREATE TABLE y {{on_cluster}} (b String) ENGINE = {{engine_mergetree}}")
	// No ON CLUSTER clause, plain (non-Replicated) engines.
	if strings.Contains(out, "ON CLUSTER") {
		t.Errorf("standalone render must not emit ON CLUSTER: %q", out)
	}
	if strings.Contains(out, "Replicated") {
		t.Errorf("standalone render must use plain engines, not Replicated*: %q", out)
	}
	if !strings.Contains(out, "ReplacingMergeTree(event_ts)") {
		t.Errorf("expected ReplacingMergeTree(event_ts): %q", out)
	}
	if !strings.Contains(out, "MergeTree()") {
		t.Errorf("expected MergeTree(): %q", out)
	}
	if strings.Contains(out, "{{") {
		t.Errorf("unsubstituted placeholder left: %q", out)
	}
}

func TestRenderClustered(t *testing.T) {
	out := Config{Cluster: "prod_ch"}.render(
		"CREATE TABLE x {{on_cluster}} (a String) ENGINE = {{engine_replacing_event_ts}}; " +
			"CREATE TABLE y {{on_cluster}} (b String) ENGINE = {{engine_mergetree}}")
	if !strings.Contains(out, "ON CLUSTER `prod_ch`") {
		t.Errorf("expected ON CLUSTER `prod_ch`: %q", out)
	}
	if !strings.Contains(out, "ReplicatedReplacingMergeTree('/clickhouse/tables/{uuid}/{shard}', '{replica}', event_ts)") {
		t.Errorf("expected Replicated replacing engine with uuid zk path: %q", out)
	}
	if !strings.Contains(out, "ReplicatedMergeTree('/clickhouse/tables/{uuid}/{shard}', '{replica}')") {
		t.Errorf("expected Replicated mergetree engine: %q", out)
	}
	if strings.Contains(out, "{{") {
		t.Errorf("unsubstituted placeholder left: %q", out)
	}
}

func TestSplitStatements(t *testing.T) {
	sql := `-- a leading comment
CREATE TABLE a (x String) ENGINE = MergeTree() ORDER BY x;

-- trailing comment only chunk
CREATE TABLE b (y String) ENGINE = MergeTree() ORDER BY y;
-- dangling comment, no statement
`
	got := splitStatements(sql)
	if len(got) != 2 {
		t.Fatalf("expected 2 statements, got %d: %#v", len(got), got)
	}
	// Comments are stripped from executable SQL (a `;` inside a comment must not
	// sever a statement).
	if strings.Contains(got[0], "--") || !strings.Contains(got[0], "CREATE TABLE a") {
		t.Errorf("statement 0 unexpected: %q", got[0])
	}
	if strings.Contains(got[1], "--") || !strings.Contains(got[1], "CREATE TABLE b") {
		t.Errorf("statement 1 unexpected: %q", got[1])
	}
}

// TestMigrationFilesHygiene enforces the migration DDL hygiene rules structurally: the
// migration SQL must never carry a literal cluster name, never CREATE OR REPLACE
// a view (NFS/EFS-unsafe), and every CREATE must be IF NOT EXISTS (idempotent).
func TestMigrationFilesHygiene(t *testing.T) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no migration files embedded")
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		// Scan executable SQL only — strip `--` line comments so the guards don't
		// trip on prose that legitimately names the very anti-patterns it forbids.
		var code strings.Builder
		for _, line := range strings.Split(string(raw), "\n") {
			if i := strings.Index(line, "--"); i >= 0 {
				line = line[:i]
			}
			code.WriteString(line)
			code.WriteString("\n")
		}
		sql := code.String()
		lower := strings.ToLower(sql)

		// Cluster name is templated, never a literal ON CLUSTER <name>.
		if strings.Contains(lower, "on cluster") {
			t.Errorf("%s: literal ON CLUSTER found — cluster must be the {{on_cluster}} placeholder (R-CH1)", e.Name())
		}
		// No CREATE OR REPLACE VIEW (not atomic on NFS/EFS).
		if strings.Contains(lower, "create or replace") {
			t.Errorf("%s: CREATE OR REPLACE is NFS/EFS-unsafe — use DROP + CREATE (R-CH2)", e.Name())
		}
		// Every CREATE TABLE/VIEW is IF NOT EXISTS (idempotent re-apply).
		for _, line := range strings.Split(lower, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "create table") || strings.HasPrefix(line, "create view") {
				if !strings.Contains(line, "if not exists") {
					t.Errorf("%s: non-idempotent DDL (missing IF NOT EXISTS): %q (R-CH2)", e.Name(), line)
				}
			}
		}
	}
}
