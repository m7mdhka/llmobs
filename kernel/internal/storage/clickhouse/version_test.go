package clickhouse

import (
	"strings"
	"testing"
	"time"
)

// TestCheckVersionFloor is the hermetic proof of the #88 fail-loud guard: a ClickHouse
// below the erasure floor must be refused (booting on it could read GDPR-erased spans
// back), and a version at/above the floor must pass. The comparison is the load-bearing
// safety decision, so it is tested away from any live server.
func TestCheckVersionFloor(t *testing.T) {
	tests := []struct {
		version string
		wantErr bool
	}{
		{"23.8.1.100", false}, // exactly the floor
		{"23.9.0.1", false},
		{"24.3.1.2823", false},
		{"25.4.2.10", false},
		{"23.7.9.999", true}, // one minor below the floor
		{"23.3.1.1", true},   // lightweight-delete GA but below our reliable floor
		{"22.8.1.1", true},   // old LTS
		{"garbage", true},    // unparseable → refuse, never assume support
		{"", true},
	}
	for _, tc := range tests {
		t.Run(tc.version, func(t *testing.T) {
			err := checkVersionFloor(tc.version)
			if tc.wantErr && err == nil {
				t.Fatalf("version %q must be refused (below floor or unparseable)", tc.version)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("version %q must be accepted, got %v", tc.version, err)
			}
			// A below-floor refusal must name the erasure reason; an unparseable version
			// is refused too but with a parse message (never assumed-supported).
			if tc.wantErr && err != nil &&
				!strings.Contains(err.Error(), "erasure") && !strings.Contains(err.Error(), "unrecognized") {
				t.Fatalf("refusal must explain itself, got %v", err)
			}
		})
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in       string
		maj, min int
		wantErr  bool
	}{
		{"24.3.1.2823", 24, 3, false},
		{"23.8", 23, 8, false},
		{" 25.4.100 ", 25, 4, false},
		{"24", 0, 0, true},
		{"a.b.c", 0, 0, true},
	}
	for _, tc := range tests {
		maj, min, err := parseVersion(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%q: expected parse error", tc.in)
			}
			continue
		}
		if err != nil || maj != tc.maj || min != tc.min {
			t.Fatalf("%q: got (%d,%d,%v), want (%d,%d,nil)", tc.in, maj, min, err, tc.maj, tc.min)
		}
	}
}

// TestReadGuardPinsDeletedMask proves — hermetically, by construction — that EVERY read
// carries apply_deleted_mask=1 (#88): readGuard is the single seam all reads obtain
// settings from, so a settings string without the mask would mean some read could
// return an erased row. The lazy-materialization guard appears only when feature-detected.
func TestReadGuardPinsDeletedMask(t *testing.T) {
	base := ReadLimits{
		MaxExecutionTime: 30 * time.Second,
		MaxMemoryUsage:   1 << 30,
		MaxRowsToRead:    1_000_000,
		MaxBytesToRead:   1 << 30,
	}

	t.Run("mask pinned, no lazy-mat guard when unsupported", func(t *testing.T) {
		s := &Store{limits: base}
		got, err := s.readGuard()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "apply_deleted_mask=1") {
			t.Fatalf("every read must pin apply_deleted_mask=1, got %q", got)
		}
		if strings.Contains(got, "query_plan_optimize_lazy_materialization") {
			t.Fatalf("lazy-mat guard must be absent when the server lacks the setting, got %q", got)
		}
	})

	t.Run("lazy-mat guard added when the server supports it", func(t *testing.T) {
		s := &Store{limits: base, guardLazyMaterialization: true}
		got, err := s.readGuard()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "apply_deleted_mask=1") {
			t.Fatalf("mask still pinned, got %q", got)
		}
		if !strings.Contains(got, "query_plan_optimize_lazy_materialization=0") {
			t.Fatalf("lazy-mat guard must be present when supported, got %q", got)
		}
	})

	t.Run("fail-closed when resource caps are unset (never an unbounded read)", func(t *testing.T) {
		s := &Store{} // no limits
		if _, err := s.readGuard(); err == nil {
			t.Fatal("readGuard must fail closed without resource caps")
		}
	})
}

// TestMutationSettingsBoundsDelete proves the #111 erasure DELETE carries an
// execution-time cap when configured (so a large delete can't hang unbounded), and is
// empty when unset.
func TestMutationSettingsBoundsDelete(t *testing.T) {
	s := &Store{limits: ReadLimits{MaxExecutionTime: 45 * time.Second}}
	if got := s.mutationSettings(); !strings.Contains(got, "max_execution_time=45") {
		t.Fatalf("erasure DELETE must carry the execution-time cap, got %q", got)
	}
	if got := (&Store{}).mutationSettings(); got != "" {
		t.Fatalf("no cap configured → empty settings, got %q", got)
	}
}
