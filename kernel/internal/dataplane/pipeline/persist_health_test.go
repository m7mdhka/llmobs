package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/ingesthealth"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// failStore is a TelemetryStore whose PersistSpan fails on demand. Embedding the
// interface (nil) satisfies the type; only PersistSpan is exercised here.
type failStore struct {
	storage.TelemetryStore
	fail bool
}

func (f *failStore) PersistSpan(context.Context, storage.Event) error {
	if f.fail {
		return errors.New("disk full")
	}
	return nil
}

// TestPersistStageFeedsHealthSignal proves the persist stage folds storage
// outcomes into the shared signal (the unit that makes /readyz flip): the
// signal stays healthy below the failure threshold, flips at it, and recovers on
// the first success.
func TestPersistStageFeedsHealthSignal(t *testing.T) {
	sig := ingesthealth.New(2)
	st := &failStore{fail: true}
	ps := &persistStage{store: st, signal: sig} // metrics nil

	ing := &Ingestion{Events: []storage.Event{{Op: storage.OpUpsert, EventID: "s1", Payload: map[string]any{}}}}

	if err := ps.Process(context.Background(), ing); err == nil {
		t.Fatal("expected persist failure")
	}
	if !sig.Healthy() {
		t.Fatal("one failure is below threshold; should stay healthy")
	}
	_ = ps.Process(context.Background(), ing) // 2nd consecutive failure => flip
	if sig.Healthy() {
		t.Fatal("threshold reached; should be unhealthy")
	}

	st.fail = false
	if err := ps.Process(context.Background(), ing); err != nil {
		t.Fatalf("persist should now succeed: %v", err)
	}
	if !sig.Healthy() {
		t.Fatal("success should recover persist health")
	}
}
