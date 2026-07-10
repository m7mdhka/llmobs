// Package jobs is the plugin job scheduler (H6): cron + @every + on-demand, with
// retries, long-running-job awareness, and audit-distinguishable system identity.
// Postgres-backed, advisory-lock leader-elected — zero new infrastructure.
package jobs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Due reports whether a schedule is due at `now` given the last run time. Two
// forms are supported:
//   - "@every <duration>"  — due when now-lastRun >= duration (e.g. "@every 1h").
//   - 5-field cron "min hour dom month dow" with *, */step, a, a-b, a,b — due when
//     `now` matches the spec AND we have not already run in the current minute.
//
// An empty schedule is never due (on-demand only). A zero lastRun means "never
// run": @every fires immediately; cron fires on the next matching minute.
func Due(schedule string, now, lastRun time.Time) (bool, error) {
	schedule = strings.TrimSpace(schedule)
	if schedule == "" {
		return false, nil
	}
	now = now.UTC().Truncate(time.Second)
	if rest, ok := strings.CutPrefix(schedule, "@every "); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil || d <= 0 {
			return false, fmt.Errorf("jobs: bad @every duration %q", rest)
		}
		if lastRun.IsZero() {
			return true, nil
		}
		return now.Sub(lastRun) >= d, nil
	}
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return false, fmt.Errorf("jobs: cron must have 5 fields, got %d in %q", len(fields), schedule)
	}
	match, err := cronMatches(fields, now)
	if err != nil {
		return false, err
	}
	if !match {
		return false, nil
	}
	// Minute-granularity dedupe: don't run twice in the same minute.
	if !lastRun.IsZero() && lastRun.UTC().Truncate(time.Minute).Equal(now.Truncate(time.Minute)) {
		return false, nil
	}
	return true, nil
}

func cronMatches(fields []string, t time.Time) (bool, error) {
	checks := []struct {
		field         string
		val, min, max int
	}{
		{fields[0], t.Minute(), 0, 59},
		{fields[1], t.Hour(), 0, 23},
		{fields[2], t.Day(), 1, 31},
		{fields[3], int(t.Month()), 1, 12},
		{fields[4], int(t.Weekday()), 0, 6},
	}
	for _, c := range checks {
		ok, err := fieldMatches(c.field, c.val, c.min, c.max)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// fieldMatches evaluates one cron field against a value.
func fieldMatches(field string, val, min, max int) (bool, error) {
	for _, part := range strings.Split(field, ",") {
		ok, err := partMatches(part, val, min, max)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func partMatches(part string, val, min, max int) (bool, error) {
	step := 1
	if base, s, ok := strings.Cut(part, "/"); ok {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return false, fmt.Errorf("jobs: bad step %q", part)
		}
		step = n
		part = base
	}
	lo, hi := min, max
	switch {
	case part == "*":
		// full range with step
	case strings.Contains(part, "-"):
		a, b, _ := strings.Cut(part, "-")
		var err error
		if lo, err = strconv.Atoi(a); err != nil {
			return false, fmt.Errorf("jobs: bad range %q", part)
		}
		if hi, err = strconv.Atoi(b); err != nil {
			return false, fmt.Errorf("jobs: bad range %q", part)
		}
	default:
		n, err := strconv.Atoi(part)
		if err != nil {
			return false, fmt.Errorf("jobs: bad cron value %q", part)
		}
		lo, hi = n, n
	}
	if val < lo || val > hi {
		return false, nil
	}
	return (val-lo)%step == 0, nil
}
