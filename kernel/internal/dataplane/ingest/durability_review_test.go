package ingest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/pipeline"
)

// errRunner returns a fixed error from Run (nil = success).
type errRunner struct{ err error }

func (r errRunner) Run(context.Context, *pipeline.Ingestion) error { return r.err }

func nopReceiver(t *testing.T, sp Spool, run Runner) *Receiver {
	t.Helper()
	r := NewReceiver(run, slog.New(slog.NewTextHandler(io.Discard, nil)), 16, 1, nil)
	r.SetSpool(sp)
	return r
}

// TestTransientFailureNeverDropsDurableData is the CRITICAL fix: a transient
// (DB-down) persist error must NEVER dead-letter or commit a durable record — that
// would advance the watermark and vaporize it on the next truncate. It must be
// requeued and survive.
func TestTransientFailureNeverDropsDurableData(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	sp, err := newWALSpool(dir, 16, 0, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	waitReplay(sp)

	r := nopReceiver(t, sp, errRunner{err: errors.New("connection refused: db down")})
	if err := sp.Append(mkjob("s1")); err != nil {
		t.Fatal(err)
	}
	l, _ := sp.tryNext()

	r.handle(context.Background(), l) // transient failure

	if sp.deadLettered.Load() != 0 {
		t.Fatal("a transient failure must NOT dead-letter a durable record")
	}
	if wm := watermarkOf(sp); wm != 0 {
		t.Fatalf("transient failure must NOT advance the watermark, got %d", wm)
	}
	if sp.inFlight.Load() != 1 {
		t.Fatalf("record must stay in-flight (durable, uncommitted), inFlight=%d", sp.inFlight.Load())
	}
	// It was requeued for retry.
	if _, ok := sp.tryNext(); !ok {
		t.Fatal("transient failure must requeue the record for retry")
	}
}

// TestPermanentFailureDeadLetters proves the flip side: a malformed body
// (pipeline.ErrPermanent) is dropped so it can't wedge the watermark forever.
func TestPermanentFailureDeadLetters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	sp, _ := newWALSpool(dir, 16, 0, nil, time.Hour)
	defer sp.Close()
	waitReplay(sp)

	r := nopReceiver(t, sp, errRunner{err: errors.Join(pipeline.ErrPermanent, errors.New("malformed OTLP"))})
	_ = sp.Append(mkjob("bad"))
	l, _ := sp.tryNext()

	r.handle(context.Background(), l)

	if sp.deadLettered.Load() != 1 {
		t.Fatalf("a permanent failure must dead-letter, got %d", sp.deadLettered.Load())
	}
	if watermarkOf(sp) != l.seq {
		t.Fatal("dead-letter must advance the watermark past the poison record")
	}
}

// TestBigBacklogReplayDoesNotDeadlock is the HIGH fix: a post-crash backlog larger
// than the channel capacity must not hang the constructor (no consumer yet). The
// replay runs in a goroutine; the constructor returns promptly and all records
// eventually drain.
func TestBigBacklogReplayDoesNotDeadlock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	// Build a backlog of 50 uncommitted records with a roomy spool.
	s1, err := newWALSpool(dir, 100, 0, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if err := s1.Append(mkjob("rec")); err != nil {
			t.Fatal(err)
		}
	}
	_ = s1.Close()

	// Reopen with a SMALL capacity (4) — far smaller than the 50-record backlog. The
	// constructor must return without blocking.
	done := make(chan *walSpool, 1)
	go func() {
		s2, e := newWALSpool(dir, 4, 0, nil, time.Hour)
		if e != nil {
			t.Errorf("reopen: %v", e)
			done <- nil
			return
		}
		done <- s2
	}()
	var s2 *walSpool
	select {
	case s2 = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("newWALSpool hung on a backlog larger than capacity (boot deadlock)")
	}
	if s2 == nil {
		return
	}
	defer s2.Close()

	// Drain all 50 — the replay producer paces itself against consumption.
	got := 0
	deadline := time.Now().Add(5 * time.Second)
	for got < 50 && time.Now().Before(deadline) {
		if l, ok := s2.tryNext(); ok {
			s2.Commit(l)
			got++
		} else {
			time.Sleep(time.Millisecond)
		}
	}
	if got != 50 {
		t.Fatalf("expected all 50 backlog records to replay, got %d", got)
	}
}

// TestUndecodableRecordDoesNotBrickBoot is the MEDIUM fix: a CRC-valid but
// undecodable record must be skipped (counted), never a hard boot failure that
// crash-loops the node.
func TestUndecodableRecordDoesNotBrickBoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	// Write two good records and one CRC-valid-but-undecodable record straight to a
	// raw WAL (a 4-byte payload is too short for a job — decodeJob rejects it).
	w, err := openWAL(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.append(encodeJob(mkjob("good1"))); err != nil {
		t.Fatal(err)
	}
	if _, err := w.append([]byte{1, 2, 3, 4}); err != nil { // valid CRC, invalid job
		t.Fatal(err)
	}
	if _, err := w.append(encodeJob(mkjob("good2"))); err != nil {
		t.Fatal(err)
	}
	_ = w.close()

	s, err := newWALSpool(dir, 16, 0, nil, time.Hour)
	if err != nil {
		t.Fatalf("one undecodable record must not fail boot: %v", err)
	}
	defer s.Close()
	waitReplay(s)

	got := map[string]bool{}
	for {
		l, ok := s.tryNext()
		if !ok {
			break
		}
		got[string(l.j.body)] = true
	}
	if !got["good1"] || !got["good2"] || len(got) != 2 {
		t.Fatalf("good records must replay around the corrupt one, got %v", got)
	}
	if s.deadLettered.Load() != 1 {
		t.Fatalf("the undecodable record must be counted, got %d", s.deadLettered.Load())
	}
}

// TestWALDirMustBeOwnerOnly is the MEDIUM fix: the WAL holds unredacted payloads +
// bearer tokens, so a group/other-accessible dir is refused (fail-closed).
func TestWALDirMustBeOwnerOnly(t *testing.T) {
	base := t.TempDir()
	open := filepath.Join(base, "world")
	if err := os.MkdirAll(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := newWALSpool(open, 16, 0, nil, time.Hour); err == nil {
		t.Fatal("a group/other-accessible WAL dir must be refused (fail-closed)")
	}

	tight := filepath.Join(base, "owner")
	if err := os.MkdirAll(tight, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := newWALSpool(tight, 16, 0, nil, time.Hour)
	if err != nil {
		t.Fatalf("an owner-only WAL dir must be accepted: %v", err)
	}
	_ = s.Close()
}

func watermarkOf(s *walSpool) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watermark
}
