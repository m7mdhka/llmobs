package jobs

import (
	"testing"
	"time"
)

func TestDueEvery(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	// Never run => due immediately.
	if due, _ := Due("@every 1h", now, time.Time{}); !due {
		t.Fatal("@every never-run should be due")
	}
	// Ran 30m ago, interval 1h => not due.
	if due, _ := Due("@every 1h", now, now.Add(-30*time.Minute)); due {
		t.Fatal("@every within interval should not be due")
	}
	// Ran 2h ago => due.
	if due, _ := Due("@every 1h", now, now.Add(-2*time.Hour)); !due {
		t.Fatal("@every past interval should be due")
	}
	if _, err := Due("@every nonsense", now, time.Time{}); err == nil {
		t.Fatal("bad @every must error")
	}
}

func TestDueCron(t *testing.T) {
	// 2026-07-10 14:30:00 UTC — a Friday (weekday 5).
	now := time.Date(2026, 7, 10, 14, 30, 0, 0, time.UTC)
	cases := []struct {
		spec string
		want bool
	}{
		{"30 14 * * *", true},   // 14:30 daily
		{"* * * * *", true},     // every minute
		{"0 14 * * *", false},   // 14:00 only
		{"30 14 10 7 *", true},  // day 10, month 7
		{"30 14 * * 5", true},   // Friday
		{"30 14 * * 1", false},  // Monday
		{"*/15 * * * *", true},  // every 15 min: 30 matches
		{"*/20 * * * *", false}, // every 20 min: 30 does not
		{"25-35 14 * * *", true},
	}
	for _, c := range cases {
		got, err := Due(c.spec, now, time.Time{})
		if err != nil {
			t.Fatalf("%q: %v", c.spec, err)
		}
		if got != c.want {
			t.Fatalf("%q: due=%v want %v", c.spec, got, c.want)
		}
	}
	// Minute-granularity dedupe: a last run in the SAME minute => not due.
	if due, _ := Due("30 14 * * *", now, now); due {
		t.Fatal("must not run twice in the same minute")
	}
	// A run in a previous minute => due again.
	if due, _ := Due("30 14 * * *", now, now.Add(-24*time.Hour)); !due {
		t.Fatal("a prior-day run should allow today's run")
	}
	// Empty schedule (on-demand only) is never due.
	if due, _ := Due("", now, time.Time{}); due {
		t.Fatal("empty schedule must not be due")
	}
}
