package clickhouse

import (
	"context"
	"errors"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// trippedConn fails the test if any query method is reached — the fail-closed
// guard must short-circuit before touching the connection.
type trippedConn struct{ t *testing.T }

func (c trippedConn) Query(context.Context, string, ...any) (driver.Rows, error) {
	c.t.Fatal("Query reached despite unset resource limits (RULING-CH9 fail-closed violated)")
	return nil, nil
}
func (c trippedConn) Exec(context.Context, string, ...any) error {
	c.t.Fatal("Exec reached despite unset resource limits")
	return nil
}
func (c trippedConn) PrepareBatch(context.Context, string, ...driver.PrepareBatchOption) (driver.Batch, error) {
	c.t.Fatal("PrepareBatch reached despite unset resource limits")
	return nil, nil
}

// TestReadFailsClosedWithoutLimits is the fail-closed prove-the-negative: with no
// (or incomplete) resource limits, EVERY DSL read refuses to emit — it never
// reaches the database. A read that forgot a limit is a bug the adapter makes
// impossible, not a config nicety.
func TestReadFailsClosedWithoutLimits(t *testing.T) {
	limitSets := []struct {
		name string
		lim  ReadLimits
	}{
		{"none", ReadLimits{}},
		{"missing exec time", ReadLimits{MaxMemoryUsage: 1, MaxRowsToRead: 1, MaxBytesToRead: 1}},
		{"missing memory", ReadLimits{MaxExecutionTime: 1, MaxRowsToRead: 1, MaxBytesToRead: 1}},
		{"missing rows", ReadLimits{MaxExecutionTime: 1, MaxMemoryUsage: 1, MaxBytesToRead: 1}},
		{"missing bytes", ReadLimits{MaxExecutionTime: 1, MaxMemoryUsage: 1, MaxRowsToRead: 1}},
	}
	args := []any{"p"} // a valid project scoping arg, so only the limit guard can fire
	for _, ls := range limitSets {
		t.Run(ls.name, func(t *testing.T) {
			s := NewStore(trippedConn{t})
			s.SetReadLimits(ls.lim)
			ctx := context.Background()
			reads := map[string]func() error{
				"QuerySpans":       func() error { _, e := s.QuerySpans(ctx, "", args, "", 10); return e },
				"QueryScores":      func() error { _, e := s.QueryScores(ctx, "", args, "", 10); return e },
				"QueryTraces":      func() error { _, e := s.QueryTraces(ctx, "", args, "", 10); return e },
				"QueryAggregation": func() error { _, e := s.QueryAggregation(ctx, "spans", "count()", "", "", args); return e },
			}
			for name, run := range reads {
				if err := run(); !errors.Is(err, errReadLimitsUnset) {
					t.Errorf("%s: want errReadLimitsUnset, got %v", name, err)
				}
			}
		})
	}
}

// TestReadLimitsSettings checks the SETTINGS clause carries all four caps.
func TestReadLimitsSettings(t *testing.T) {
	s := ReadLimits{MaxExecutionTime: 30_000_000_000, MaxMemoryUsage: 100, MaxRowsToRead: 200, MaxBytesToRead: 300}.settings()
	for _, want := range []string{"max_execution_time=30", "max_memory_usage=100", "max_rows_to_read=200", "max_bytes_to_read=300"} {
		if !contains(s, want) {
			t.Errorf("settings %q missing %q", s, want)
		}
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
