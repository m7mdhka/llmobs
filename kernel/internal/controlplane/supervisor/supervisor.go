// Package supervisor is the plugin lifecycle state machine (ADR-0006, ADR-0023).
// It reconciles each installed backend plugin toward running: handshake -> issue
// service token -> probe two-signal health (ready + functional watermark). A
// plugin that faults (unreachable, failed handshake, unhealthy, stale watermark)
// is degraded and retried with exponential backoff; past a cap it auto-disables.
//
// The supervisor is DB-free and in-memory by construction (state is derived by
// probing); leadership (single supervising replica) is gated by the caller via
// the pg advisory-lock pattern the migration runner already proves. Token
// issuance uses the kernel signer; lifecycle decisions live here, reachability in
// the executor.
package supervisor

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/executors"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/metrics"
)

// State is a plugin's lifecycle state.
type State string

const (
	StateInstalled State = "installed"
	StateStarting  State = "starting"
	StateRunning   State = "running"
	StateDegraded  State = "degraded"
	StateDisabled  State = "disabled"
)

func stateCode(s State) float64 {
	switch s {
	case StateStarting:
		return 1
	case StateRunning:
		return 2
	case StateDegraded:
		return 3
	case StateDisabled:
		return 4
	default:
		return 0 // installed
	}
}

// Config tunes the state machine. Zero values fall back to defaults.
type Config struct {
	TokenTTL      time.Duration // service-token lifetime
	RefreshBefore time.Duration // refresh when the token is within this of expiry
	MaxFaults     int           // consecutive faults before auto-disable
	BaseBackoff   time.Duration // first retry delay
	MaxBackoff    time.Duration // backoff ceiling
}

func (c Config) withDefaults() Config {
	if c.TokenTTL <= 0 {
		c.TokenTTL = 10 * time.Minute
	}
	if c.RefreshBefore <= 0 {
		c.RefreshBefore = 3 * time.Minute
	}
	if c.MaxFaults <= 0 {
		c.MaxFaults = 5
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = time.Minute
	}
	return c
}

type pluginRuntime struct {
	spec  PluginSpec
	state State
	token string
	exp   int64 // service-token expiry (unix)

	faults          int   // consecutive faults -> backoff -> disable
	restartAttempts int   // cumulative (ops signal)
	nextAttempt     time.Time
	lastWatermark   int64
	tokenRefreshErr int
	disabledReason  string
	lastError       string
}

// Supervisor reconciles plugins toward running and exposes their status.
type Supervisor struct {
	provider Provider
	exec     executors.Executor
	signer   *plugintoken.Signer
	log      *slog.Logger
	metrics  *metrics.Registry
	cfg      Config
	now      func() time.Time // injectable clock

	mu       sync.Mutex
	plugins  map[string]*pluginRuntime
	disabled map[string]string // operator-disabled id -> reason (persists across reconcile)
}

// New builds a supervisor. now may be nil (defaults to time.Now).
func New(p Provider, exec executors.Executor, signer *plugintoken.Signer, mreg *metrics.Registry, log *slog.Logger, cfg Config, now func() time.Time) *Supervisor {
	if now == nil {
		now = time.Now
	}
	return &Supervisor{
		provider: p, exec: exec, signer: signer, metrics: mreg, log: log,
		cfg: cfg.withDefaults(), now: now,
		plugins:  map[string]*pluginRuntime{},
		disabled: map[string]string{},
	}
}

// Reconcile runs one pass over all installed plugins. It is idempotent and safe
// to call on a ticker.
func (s *Supervisor) Reconcile(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, spec := range s.provider.Plugins() {
		rt := s.plugins[spec.ID]
		if rt == nil {
			rt = &pluginRuntime{spec: spec, state: StateInstalled}
			s.plugins[spec.ID] = rt
		}
		rt.spec = spec

		if reason, off := s.disabled[spec.ID]; off {
			s.toDisabled(rt, reason)
			continue
		}
		s.reconcileOne(ctx, rt)
	}
	s.publishMetrics()
}

func (s *Supervisor) reconcileOne(ctx context.Context, rt *pluginRuntime) {
	now := s.now()
	if rt.state == StateDisabled && rt.disabledReason != "" && s.disabled[rt.spec.ID] == "" {
		// auto-disabled previously; stay disabled until an operator re-enables.
		return
	}
	if now.Before(rt.nextAttempt) {
		return // backoff gate
	}

	// Ensure a valid service token. Absence is a re-handshake TRIGGER, not a fault:
	// after a kernel restart the in-memory signing key rotates and all live tokens
	// (and our in-memory state) are gone, so a routine restart must re-handshake
	// every healthy plugin WITHOUT marching it toward auto-disable (ADR-0023).
	if rt.token == "" || now.Unix() >= rt.exp {
		rt.state = StateStarting
		info, err := s.exec.Handshake(ctx, rt.spec.Backend)
		if err != nil {
			s.fault(rt, "handshake: "+err.Error())
			return
		}
		if err := info.Check(rt.spec.ID, rt.spec.GrantedCapabilities); err != nil {
			s.fault(rt, "handshake-check: "+err.Error())
			return
		}
		tok, claims, err := s.signer.MintServiceToken(rt.spec.ID, s.scopesFor(rt.spec), now, s.cfg.TokenTTL)
		if err != nil {
			s.fault(rt, "mint-token: "+err.Error())
			return
		}
		rt.token, rt.exp = tok, claims.Exp
	} else if now.Add(s.cfg.RefreshBefore).Unix() >= rt.exp {
		// Proactive refresh while running. A refresh failure is tracked but not an
		// immediate fault — the next reconcile re-handshakes when the token lapses.
		if tok, claims, err := s.signer.MintServiceToken(rt.spec.ID, s.scopesFor(rt.spec), now, s.cfg.TokenTTL); err != nil {
			rt.tokenRefreshErr++
		} else {
			rt.token, rt.exp = tok, claims.Exp
		}
	}

	// Two-signal health.
	h, err := s.exec.Health(ctx, rt.spec.Backend)
	if err != nil {
		s.fault(rt, "health: "+err.Error())
		return
	}
	if h.Watermark != nil {
		rt.lastWatermark = h.Watermark.LastProgressUnix
	}
	if !h.Live || !h.Ready {
		reason := h.Reason
		if reason == "" {
			reason = "not ready"
		}
		s.fault(rt, reason)
		return
	}
	if h.StaleWatermark(now, rt.spec.WatermarkBudget) {
		s.fault(rt, "functional watermark stale past budget")
		return
	}
	s.markRunning(rt)
}

// fault records a plugin-side failure: it degrades the plugin and schedules a
// backoff retry, or auto-disables once the consecutive-fault cap is hit. Token
// absence never routes here (see reconcileOne) — only genuine plugin faults do.
func (s *Supervisor) fault(rt *pluginRuntime, reason string) {
	rt.faults++
	rt.restartAttempts++
	rt.lastError = reason
	if rt.faults >= s.cfg.MaxFaults {
		rt.state = StateDisabled
		rt.disabledReason = "auto-disabled after " + itoa(rt.faults) + " consecutive faults: " + reason
		rt.token, rt.exp = "", 0 // revoke; touches no plugin data
		s.log.Warn("plugin auto-disabled", "plugin_id", rt.spec.ID, "reason", rt.disabledReason)
		return
	}
	rt.state = StateDegraded
	rt.nextAttempt = s.now().Add(s.backoffFor(rt.faults))
}

func (s *Supervisor) markRunning(rt *pluginRuntime) {
	if rt.state != StateRunning {
		s.log.Info("plugin running", "plugin_id", rt.spec.ID)
	}
	rt.state = StateRunning
	rt.faults = 0
	rt.nextAttempt = time.Time{}
	rt.lastError = ""
	rt.disabledReason = ""
}

func (s *Supervisor) toDisabled(rt *pluginRuntime, reason string) {
	if rt.state != StateDisabled {
		s.log.Info("plugin disabled by operator", "plugin_id", rt.spec.ID, "reason", reason)
	}
	rt.state = StateDisabled
	rt.disabledReason = reason
	rt.token, rt.exp = "", 0 // revoke token; supervision stops; no data touched
	rt.faults = 0
}

func (s *Supervisor) backoffFor(faults int) time.Duration {
	d := s.cfg.BaseBackoff
	for i := 1; i < faults; i++ {
		d *= 2
		if d >= s.cfg.MaxBackoff {
			return s.cfg.MaxBackoff
		}
	}
	if d > s.cfg.MaxBackoff {
		d = s.cfg.MaxBackoff
	}
	return d
}

// scopesFor is the plugin's approved token scopes: its capabilities plus its
// fine-grained permissions (de-duplicated). H3 intersects these with the user's.
func (s *Supervisor) scopesFor(spec PluginSpec) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, x := range append(append([]string{}, spec.GrantedCapabilities...), spec.GrantedScopes...) {
		if _, ok := seen[x]; ok {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	return out
}

// Disable operator-disables a plugin: supervision stops and its token is revoked;
// no plugin data is touched (uninstall is a separate data decision).
func (s *Supervisor) Disable(id, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if reason == "" {
		reason = "disabled by operator"
	}
	s.disabled[id] = reason
	if rt := s.plugins[id]; rt != nil {
		s.toDisabled(rt, reason)
	}
}

// Enable clears an operator disable (or an auto-disable) so the plugin is
// supervised again from installed.
func (s *Supervisor) Enable(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.disabled, id)
	if rt := s.plugins[id]; rt != nil {
		rt.state = StateInstalled
		rt.faults = 0
		rt.disabledReason = ""
		rt.nextAttempt = time.Time{}
		rt.token, rt.exp = "", 0
	}
}

// ServiceTokenFor returns the current service token for a running plugin (used by
// the gateway proxy in H5+ to authenticate the plugin on its return path). Empty
// when the plugin is not running.
func (s *Supervisor) ServiceTokenFor(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rt := s.plugins[id]; rt != nil && rt.state == StateRunning {
		return rt.token
	}
	return ""
}

// --- ops surface ---

// PluginStatus is the ops read model.
type PluginStatus struct {
	ID                   string `json:"id"`
	State                string `json:"state"`
	Faults               int    `json:"faults"`
	RestartAttempts      int    `json:"restart_attempts"`
	LastWatermarkUnix    int64  `json:"last_watermark_unix,omitempty"`
	TokenRefreshFailures int    `json:"token_refresh_failures"`
	DisabledReason       string `json:"disabled_reason,omitempty"`
	LastError            string `json:"last_error,omitempty"`
}

// Snapshot returns the current status of every known plugin.
func (s *Supervisor) Snapshot() []PluginStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PluginStatus, 0, len(s.plugins))
	for _, rt := range s.plugins {
		out = append(out, PluginStatus{
			ID: rt.spec.ID, State: string(rt.state), Faults: rt.faults,
			RestartAttempts: rt.restartAttempts, LastWatermarkUnix: rt.lastWatermark,
			TokenRefreshFailures: rt.tokenRefreshErr, DisabledReason: rt.disabledReason,
			LastError: rt.lastError,
		})
	}
	return out
}

func (s *Supervisor) publishMetrics() {
	if s.metrics == nil {
		return
	}
	for _, rt := range s.plugins {
		lbl := map[string]string{"plugin_id": rt.spec.ID}
		running := 0.0
		if rt.state == StateRunning {
			running = 1
		}
		s.metrics.GaugeSet("llmobs_plugin_running", "1 when a plugin backend is running, else 0.", lbl, running)
		s.metrics.GaugeSet("llmobs_plugin_state_code", "Plugin lifecycle state (0=installed,1=starting,2=running,3=degraded,4=disabled).", lbl, stateCode(rt.state))
		s.metrics.GaugeSet("llmobs_plugin_restart_attempts", "Cumulative supervisor restart attempts for a plugin.", lbl, float64(rt.restartAttempts))
		s.metrics.GaugeSet("llmobs_plugin_token_refresh_failures", "Cumulative service-token refresh failures for a plugin.", lbl, float64(rt.tokenRefreshErr))
		if rt.lastWatermark > 0 {
			s.metrics.GaugeSet("llmobs_plugin_last_watermark_unix", "Last functional-watermark timestamp reported by a plugin.", lbl, float64(rt.lastWatermark))
		}
	}
}

// Handler serves the ops API: GET /plugins (snapshot) and POST
// /plugins/{id}/disable | /enable (gated by isAllowed for mutations). Mount under
// the authenticated API mux; isAllowed decides who may mutate (admin).
func (s *Supervisor) Handler(prefix string, isAllowed func(*http.Request) bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(prefix+"/plugins", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"plugins": s.Snapshot()})
	})
	mux.HandleFunc(prefix+"/plugins/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if isAllowed != nil && !isAllowed(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, prefix+"/plugins/")
		id, action, ok := lastSegment(rest)
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch action {
		case "disable":
			s.Disable(id, "disabled via ops API")
		case "enable":
			s.Enable(id)
		default:
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "action": action})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// lastSegment splits "owner/name/action" into ("owner/name","action").
func lastSegment(p string) (string, string, bool) {
	i := strings.LastIndex(p, "/")
	if i <= 0 || i == len(p)-1 {
		return "", "", false
	}
	return p[:i], p[i+1:], true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
