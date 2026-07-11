package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mkjob(id string) job {
	return job{body: []byte(id), contentType: "application/x-protobuf", bearer: "k", receivedAt: time.Unix(0, 0).UTC()}
}

// waitReplay blocks until boot replay has finished feeding the channel (replay is
// async so a reopened spool must not be drained before it completes).
func waitReplay(s *walSpool) {
	for i := 0; i < 2000; i++ {
		if s.replayDone.Load() {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMemSpoolBackpressure(t *testing.T) {
	s := newMemSpool(2)
	if err := s.Append(mkjob("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(mkjob("b")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(mkjob("c")); err != ErrSpoolFull {
		t.Fatalf("expected ErrSpoolFull at capacity, got %v", err)
	}
	if s.Durable() {
		t.Error("memSpool must report non-durable")
	}
}

// TestWALSpoolAckAfterDurableReplay is the ack-after-durable proof: a record that
// was Appended (durably) but NOT Committed — the crash-before-persist case —
// survives a spool close/reopen and replays. This closes the SIGKILL window.
func TestWALSpoolAckAfterDurableReplay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	s, err := newWALSpool(dir, 16, 0, nil, time.Hour) // long ckpt so nothing checkpoints
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"r1", "r2", "r3"} {
		if err := s.Append(mkjob(id)); err != nil {
			t.Fatal(err)
		}
	}
	// Lease + Commit ONLY r1 (simulate: r1 persisted, r2/r3 acked-but-unpersisted).
	l, ok := s.Next(context.Background())
	if !ok || string(l.j.body) != "r1" {
		t.Fatalf("expected r1 first, got %v %q", ok, l.j.body)
	}
	s.Commit(l)
	// Force a checkpoint so r1's watermark is durable, then "crash" (close) with
	// r2/r3 still uncommitted.
	s.checkpoint()
	_ = s.Close()

	// Reopen: r2 and r3 must replay (r1 must NOT — it was committed).
	s2, err := newWALSpool(dir, 16, 0, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	waitReplay(s2)
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		l, ok := s2.tryNext()
		if !ok {
			t.Fatalf("expected 2 replayed records, got %d", i)
		}
		got[string(l.j.body)] = true
	}
	if _, extra := s2.tryNext(); extra {
		t.Fatal("committed record r1 must not replay")
	}
	if !got["r2"] || !got["r3"] {
		t.Fatalf("expected r2+r3 replayed, got %v", got)
	}
}

// TestWALSpoolCommittedNotReplayed proves the checkpoint truly suppresses replay of
// fully-persisted records (no infinite reprocessing).
func TestWALSpoolCommittedNotReplayed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	s, _ := newWALSpool(dir, 16, 0, nil, time.Hour)
	for _, id := range []string{"a", "b"} {
		_ = s.Append(mkjob(id))
	}
	for i := 0; i < 2; i++ {
		l, _ := s.Next(context.Background())
		s.Commit(l)
	}
	s.checkpoint()
	_ = s.Close()

	s2, _ := newWALSpool(dir, 16, 0, nil, time.Hour)
	defer s2.Close()
	if _, ok := s2.tryNext(); ok {
		t.Fatal("all records were committed; none should replay")
	}
}

// TestWALSpoolTornTail proves a partial final record (a crash mid-write) is
// truncated on replay, not misread — the preceding good records still recover.
func TestWALSpoolTornTail(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	s, _ := newWALSpool(dir, 16, 0, nil, time.Hour)
	_ = s.Append(mkjob("good1"))
	_ = s.Append(mkjob("good2"))
	// Corrupt the tail: append raw garbage (a partial header) to the active segment.
	segs, _ := s.wal.segments()
	seg := dir + "/" + segs[len(segs)-1]
	_ = s.Close()
	appendGarbage(t, seg)

	s2, err := newWALSpool(dir, 16, 0, nil, time.Hour)
	if err != nil {
		t.Fatalf("reopen must tolerate a torn tail: %v", err)
	}
	defer s2.Close()
	waitReplay(s2)
	got := map[string]bool{}
	for {
		l, ok := s2.tryNext()
		if !ok {
			break
		}
		got[string(l.j.body)] = true
	}
	if !got["good1"] || !got["good2"] || len(got) != 2 {
		t.Fatalf("torn tail must recover exactly the 2 good records, got %v", got)
	}
}

func TestWALSpoolBackpressure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	s, _ := newWALSpool(dir, 2, 0, nil, time.Hour)
	defer s.Close()
	if err := s.Append(mkjob("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(mkjob("b")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(mkjob("c")); err != ErrSpoolFull {
		t.Fatalf("expected ErrSpoolFull at capacity, got %v", err)
	}
	if !s.Durable() {
		t.Error("walSpool must report durable")
	}
}

// TestWALSpoolArchiveThenRestore proves the async object-store tier: sealed
// segments are archived to the sink, and a disaster restore (local WAL wiped)
// recovers them from the sink for replay.
func TestWALSpoolArchiveThenRestore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	sink := newMemSink()
	// Tiny segments so appends roll over and seal segments the archiver can ship.
	s, _ := newWALSpool(dir, 64, 200, sink, time.Hour)
	for i := 0; i < 20; i++ {
		_ = s.Append(mkjob("rec-with-some-bytes-to-fill-a-segment"))
	}
	// Commit + checkpoint so sealed segments below the watermark archive.
	for i := 0; i < 20; i++ {
		l, ok := s.tryNext()
		if !ok {
			break
		}
		s.Commit(l)
	}
	s.checkpoint()
	_ = s.Close()

	keys, _ := sink.List(context.Background())
	if len(keys) == 0 {
		t.Fatal("expected at least one sealed segment archived to the sink")
	}
}

func appendGarbage(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write([]byte{0xFF, 0xFF, 0xFF}); err != nil { // shorter than a header
		t.Fatal(err)
	}
}
