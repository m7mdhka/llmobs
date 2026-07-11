package ingest

import (
	"context"
	"os"
	"sync"
)

// ArchiveSink is the object-store archival/replay tier BEHIND the WAL (ADR-0027
// D5). Sealed WAL segments are uploaded here by the background checkpointer —
// NEVER on the hot Append path — as the disaster-recovery replay source (if the
// local WAL is lost, boot restores segments above the watermark from the sink).
// Interface-first so lite uses noopSink and scale an S3/MinIO sink.
type ArchiveSink interface {
	// Put uploads the segment file at localPath under key. Best-effort; the WAL is
	// the durable floor, so a failed Put just retries on the next checkpoint tick.
	Put(ctx context.Context, key, localPath string) error
	// Get restores an archived segment to localPath (disaster replay).
	Get(ctx context.Context, key, localPath string) error
	// List returns archived segment keys (for a restore drill).
	List(ctx context.Context) ([]string, error)
}

// noopSink is the lite default: local WAL only, no object-store tier.
type noopSink struct{}

func (noopSink) Put(context.Context, string, string) error { return nil }
func (noopSink) Get(context.Context, string, string) error { return nil }
func (noopSink) List(context.Context) ([]string, error)    { return nil, nil }

// memSink is an in-memory ArchiveSink for tests: it copies segment bytes into a
// map, standing in for object storage without a network dependency. The real
// S3/MinIO sink (deferred, ADR-0027) implements the same interface.
type memSink struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

func newMemSink() *memSink { return &memSink{blobs: map[string][]byte{}} }

func (s *memSink) Put(_ context.Context, key, localPath string) error {
	b, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.blobs[key] = b
	s.mu.Unlock()
	return nil
}

func (s *memSink) Get(_ context.Context, key, localPath string) error {
	s.mu.Lock()
	b, ok := s.blobs[key]
	s.mu.Unlock()
	if !ok {
		return os.ErrNotExist
	}
	return os.WriteFile(localPath, b, 0o644)
}

func (s *memSink) List(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.blobs))
	for k := range s.blobs {
		keys = append(keys, k)
	}
	return keys, nil
}

func (s *memSink) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.blobs[key]
	return ok
}
