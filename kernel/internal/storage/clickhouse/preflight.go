package clickhouse

import (
	"context"
	"fmt"
	"strings"
)

// preflightTable is the throwaway table the functional preflight exercises. It is
// dropped at the end (and pre-dropped for idempotency), so a leftover from a
// crashed run never blocks a retry.
const preflightTable = "llmobs_migration_preflight"

// preflightConn is the narrow surface the preflight needs.
type preflightConn interface {
	Exec(ctx context.Context, query string, args ...any) error
}

// Preflight validates the migration/write grant set FUNCTIONALLY (R-CH8): rather
// than parse ClickHouse's privilege hierarchy out of SHOW GRANTS (CREATE implies
// CREATE TABLE, ALL implies everything — brittle to reproduce), it performs each
// operation the adapter needs on a throwaway table and maps any access failure
// back to the exact privilege to grant. It fails early, naming what is missing,
// instead of letting a mid-migration access error surface opaquely.
//
// Preflight is standalone mode only (no ON CLUSTER): it probes the local node's
// grants, which is what a migration needs to begin.
func Preflight(ctx context.Context, db preflightConn, database string) error {
	qualified := fmt.Sprintf("`%s`.`%s`", database, preflightTable)

	// Best-effort clean slate; ignore the error (a missing table or a missing DROP
	// grant both surface below where they can be attributed).
	_ = db.Exec(ctx, "DROP TABLE IF EXISTS "+qualified)

	type step struct {
		priv string
		sql  string
	}
	steps := []step{
		{"CREATE TABLE", fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (x UInt8) ENGINE = MergeTree() ORDER BY x", qualified)},
		{"INSERT", fmt.Sprintf("INSERT INTO %s (x) VALUES (1)", qualified)},
		{"SELECT", fmt.Sprintf("SELECT count() FROM %s", qualified)},
		{"ALTER", fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS y UInt8", qualified)},
		{"OPTIMIZE", fmt.Sprintf("OPTIMIZE TABLE %s FINAL", qualified)},
	}

	var missing []string
	for _, s := range steps {
		if err := db.Exec(ctx, s.sql); err != nil {
			if isAccessDenied(err) {
				missing = append(missing, s.priv)
				continue
			}
			return fmt.Errorf("preflight %s: %w", s.priv, err)
		}
	}

	// DROP validates the last privilege AND cleans up. Attempt it regardless of
	// earlier failures so we never leak the probe table.
	if err := db.Exec(ctx, "DROP TABLE IF EXISTS "+qualified); err != nil {
		if isAccessDenied(err) {
			missing = append(missing, "DROP TABLE")
		} else {
			return fmt.Errorf("preflight DROP TABLE: %w", err)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("clickhouse user is missing privileges %v — grant them: %s",
			missing, grantHint(database))
	}
	return nil
}

// isAccessDenied reports whether an error is a ClickHouse access-control denial
// (error code 497, ACCESS_DENIED) rather than a connectivity/syntax failure.
func isAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "ACCESS_DENIED") ||
		strings.Contains(msg, "Not enough privileges") ||
		strings.Contains(msg, "code: 497")
}
