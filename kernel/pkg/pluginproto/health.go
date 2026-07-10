package pluginproto

import "time"

// Watermark is the functional-progress signal in a plugin's health report:
// evidence that work is actually happening, not just that the process is up.
type Watermark struct {
	LastProgressUnix int64  `json:"lastProgressUnix"`
	Detail           string `json:"detail,omitempty"`
}

// Health is a plugin's two-signal health report (GET /plugin/v1/health),
// validated against api/plugin/v1alpha1/health.schema.json.
type Health struct {
	Live      bool       `json:"live"`
	Ready     bool       `json:"ready"`
	Watermark *Watermark `json:"watermark,omitempty"`
	Reason    string     `json:"reason,omitempty"`
}

// StaleWatermark reports whether a (ready) plugin should nonetheless be treated as
// degraded because its functional watermark has gone stale past budget. A
// non-positive budget, or an absent watermark, means the watermark is not used for
// the running/degraded decision — so a plugin that reports no watermark is never
// degraded on this basis, and a busy long-running job that keeps advancing its
// watermark never reads as degraded merely for being busy.
func (h Health) StaleWatermark(now time.Time, budget time.Duration) bool {
	if budget <= 0 || h.Watermark == nil {
		return false
	}
	last := time.Unix(h.Watermark.LastProgressUnix, 0)
	return now.Sub(last) > budget
}
