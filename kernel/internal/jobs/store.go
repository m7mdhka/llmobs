package jobs

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Run is one job execution record (the audit trail of job-initiated activity, so
// scheduled-vs-on-behalf-of-user is always answerable — H6 pin 2). Actor is
// "system:job:<plugin>:<job>" for scheduled runs and "session:<email>" for
// on-demand triggers.
type Run struct {
	ID         string    `json:"id"`
	PluginID   string    `json:"plugin_id"`
	Job        string    `json:"job"`
	Trigger    string    `json:"trigger"` // "schedule" | "on_demand"
	Actor      string    `json:"actor"`
	Status     string    `json:"status"` // "running" | "succeeded" | "failed"
	Attempts   int       `json:"attempts"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// Store persists job runs. The scheduler's logic is backend-agnostic over this.
type Store interface {
	StartRun(ctx context.Context, r Run) error
	FinishRun(ctx context.Context, id, status, errMsg string, attempts int) error
	// IsRunning reports whether a run of (plugin, job) is in flight — the
	// long-running-job guard against double-triggering.
	IsRunning(ctx context.Context, pluginID, job string) (bool, error)
	// LastRun returns the most recent start time of (plugin, job), or zero.
	LastRun(ctx context.Context, pluginID, job string) (time.Time, error)
	RecentRuns(ctx context.Context, pluginID string, limit int) ([]Run, error)
}

// MemStore is an in-memory Store for tests (non-durable).
type MemStore struct {
	mu   sync.Mutex
	runs map[string]Run
}

func NewMemStore() *MemStore { return &MemStore{runs: map[string]Run{}} }

func (m *MemStore) StartRun(_ context.Context, r Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[r.ID] = r
	return nil
}

func (m *MemStore) FinishRun(_ context.Context, id, status, errMsg string, attempts int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runs[id]
	r.Status = status
	r.Error = errMsg
	r.Attempts = attempts
	r.FinishedAt = time.Unix(r.StartedAt.Unix()+1, 0).UTC()
	m.runs[id] = r
	return nil
}

func (m *MemStore) IsRunning(_ context.Context, pluginID, job string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runs {
		if r.PluginID == pluginID && r.Job == job && r.Status == "running" {
			return true, nil
		}
	}
	return false, nil
}

func (m *MemStore) LastRun(_ context.Context, pluginID, job string) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest time.Time
	for _, r := range m.runs {
		if r.PluginID == pluginID && r.Job == job && r.StartedAt.After(latest) {
			latest = r.StartedAt
		}
	}
	return latest, nil
}

func (m *MemStore) RecentRuns(_ context.Context, pluginID string, limit int) ([]Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Run
	for _, r := range m.runs {
		if r.PluginID == pluginID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
