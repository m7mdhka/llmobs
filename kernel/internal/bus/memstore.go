package bus

import (
	"context"
	"sort"
	"sync"
)

// MemStore is an in-memory Store — NON-DURABLE (state is lost on restart). It is
// for tests and single-process dev only; the lite profile uses the Postgres store.
// It implements exactly the same semantics the contract test asserts, so passing
// the contract against MemStore is meaningful for any backend.
type MemStore struct {
	mu      sync.Mutex
	seq     int64
	events  []Delivered      // append-only log
	offsets map[string]int64 // key: plugin\x00project\x00topic
	dlq     []dlqEntry
}

type dlqEntry struct {
	PluginID, ProjectID, Topic, Reason string
	FromID, ToID                       int64
}

func NewMemStore() *MemStore {
	return &MemStore{offsets: map[string]int64{}}
}

func offKey(plugin, project, topic string) string { return plugin + "\x00" + project + "\x00" + topic }

func (m *MemStore) Append(_ context.Context, topic, projectID, subjectID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	m.events = append(m.events, Delivered{ID: m.seq, Topic: topic, ProjectID: projectID, SubjectID: subjectID})
	return m.seq, nil
}

func (m *MemStore) LatestID(_ context.Context, topic, projectID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest int64
	for _, e := range m.events {
		if e.Topic == topic && e.ProjectID == projectID && e.ID > latest {
			latest = e.ID
		}
	}
	return latest, nil
}

func (m *MemStore) After(_ context.Context, topic, projectID string, afterID int64, limit int) ([]Delivered, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Delivered
	for _, e := range m.events {
		if e.Topic == topic && e.ProjectID == projectID && e.ID > afterID {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) Offset(_ context.Context, pluginID, projectID, topic string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.offsets[offKey(pluginID, projectID, topic)], nil
}

func (m *MemStore) SetOffset(_ context.Context, pluginID, projectID, topic string, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.offsets[offKey(pluginID, projectID, topic)] = id
	return nil
}

func (m *MemStore) DeadLetter(_ context.Context, pluginID, projectID, topic string, fromID, toID int64, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dlq = append(m.dlq, dlqEntry{PluginID: pluginID, ProjectID: projectID, Topic: topic, FromID: fromID, ToID: toID, Reason: reason})
	return nil
}

// DLQLen exposes the dead-letter count (test assertion helper).
func (m *MemStore) DLQLen() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.dlq)
}
