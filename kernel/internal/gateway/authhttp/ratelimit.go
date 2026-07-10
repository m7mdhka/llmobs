package authhttp

import (
	"sync"
	"time"
)

// rateLimiter is a small fixed-window counter keyed by client IP, used to throttle
// login attempts. In-memory and per-process: adequate for the lite profile and a
// defense-in-depth measure, not a distributed quota. A scale deployment would
// front this with an edge limiter.
type rateLimiter struct {
	mu       sync.Mutex
	windows  map[string]*window
	limit    int
	duration time.Duration
	now      func() time.Time
}

type window struct {
	count int
	reset time.Time
}

func newRateLimiter(limit int, duration time.Duration) *rateLimiter {
	return &rateLimiter{
		windows:  map[string]*window{},
		limit:    limit,
		duration: duration,
		now:      func() time.Time { return time.Now() },
	}
}

// allow records an attempt for key and reports whether it is within the limit.
func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.now()
	w, ok := rl.windows[key]
	if !ok || now.After(w.reset) {
		rl.windows[key] = &window{count: 1, reset: now.Add(rl.duration)}
		rl.evict(now)
		return true
	}
	if w.count >= rl.limit {
		return false
	}
	w.count++
	return true
}

// evict drops expired windows opportunistically so the map cannot grow unbounded.
func (rl *rateLimiter) evict(now time.Time) {
	if len(rl.windows) < 1024 {
		return
	}
	for k, w := range rl.windows {
		if now.After(w.reset) {
			delete(rl.windows, k)
		}
	}
}
