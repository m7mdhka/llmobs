package ingest

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ErrSpoolFull is returned by Append when the in-flight buffer is at capacity —
// the receiver translates it to a retryable 503/UNAVAILABLE (G2 backpressure).
var ErrSpoolFull = errors.New("spool full")

// leased is a record handed to a worker: the job plus the WAL seq it must Commit
// once the record is durably persisted (0 for the in-memory spool, which has no
// replay and so nothing to commit). retry counts failed processing attempts.
type leased struct {
	seq   uint64
	j     job
	retry int
}

// maxProcessRetries bounds in-process retries of a failing record before it is
// dead-lettered (ADR-0027 D6) — a poison record must not block the watermark.
const maxProcessRetries = 5

// Spool is the ack-window seam (ADR-0027). The receiver Appends a received job
// (durably, for the WAL impl, so the ack is ack-after-durable), workers lease via
// Next and Commit once persisted. Two impls: memSpool (bounded channel, lite) and
// walSpool (durable floor + replay, scale).
type Spool interface {
	Append(j job) error
	Next(ctx context.Context) (leased, bool)
	tryNext() (leased, bool) // non-blocking pull, used by the drain path
	Commit(l leased)
	requeue(l leased)    // retry a failed record WITHOUT a new durable write
	deadLetter(l leased) // give up on a poison record: count + advance past it
	Len() int
	Cap() int
	// Durable reports whether an undrained record survives shutdown (WAL: replays on
	// boot, so it is NOT lost; memory: lost, counted as the residual drain window).
	Durable() bool
	Close() error
}

// ---- memSpool: the in-memory bounded channel (lite default; prior behaviour) ----

type memSpool struct {
	ch  chan leased
	cap int
}

func newMemSpool(capacity int) *memSpool {
	if capacity <= 0 {
		capacity = 1024
	}
	return &memSpool{ch: make(chan leased, capacity), cap: capacity}
}

func (m *memSpool) Append(j job) error {
	select {
	case m.ch <- leased{j: j}:
		return nil
	default:
		return ErrSpoolFull
	}
}

func (m *memSpool) Next(ctx context.Context) (leased, bool) {
	select {
	case <-ctx.Done():
		return leased{}, false
	case l, ok := <-m.ch:
		return l, ok
	}
}

func (m *memSpool) Commit(leased) {}
func (m *memSpool) Len() int      { return len(m.ch) }
func (m *memSpool) Cap() int      { return m.cap }
func (m *memSpool) Close() error  { return nil }

// requeue retries a failed record (best-effort, non-blocking); if the buffer is
// full the record is dropped — the in-memory spool has no durability floor, which
// is the pre-existing lite behaviour on a full queue.
func (m *memSpool) requeue(l leased) {
	select {
	case m.ch <- l:
	default:
	}
}

// deadLetter drops a poison record (lite has no DLQ segment; the drop is the
// terminal outcome, same as a persist error was pre-L3).
func (m *memSpool) deadLetter(leased) {}

// tryNext is a non-blocking pull used by the drain path.
func (m *memSpool) tryNext() (leased, bool) {
	select {
	case l, ok := <-m.ch:
		return l, ok
	default:
		return leased{}, false
	}
}

// ---- walSpool: the durable floor (WAL) + idempotent replay (scale) ----

type walSpool struct {
	wal      *wal
	ch       chan leased
	capacity int
	inFlight atomic.Int64

	mu        sync.Mutex
	watermark uint64          // highest contiguously-persisted seq (checkpointed)
	done      map[uint64]bool // committed seqs above the watermark

	archive      ArchiveSink
	stopCh       chan struct{}
	stopOnce     sync.Once
	wg           sync.WaitGroup
	ckptEvery    time.Duration
	deadLettered atomic.Int64
}

// NewWALSpool builds the durable WAL-backed spool for the scale profile (ADR-0027).
// dir is the local WAL directory; archive is the optional object-store tier (nil =
// local-only durable floor). The receiver swaps it in via SetSpool.
func NewWALSpool(dir string, capacity int, maxSeg int64, archive ArchiveSink, ckptEvery time.Duration) (Spool, error) {
	return newWALSpool(dir, capacity, maxSeg, archive, ckptEvery)
}

// newWALSpool opens the WAL at dir and replays every record above the checkpoint
// watermark into the in-flight buffer (idempotent — duplicates re-fold safely).
// archive may be nil (local-only durable floor; no object-store tier).
func newWALSpool(dir string, capacity int, maxSeg int64, archive ArchiveSink, ckptEvery time.Duration) (*walSpool, error) {
	if capacity <= 0 {
		capacity = 1024
	}
	if ckptEvery <= 0 {
		ckptEvery = time.Second
	}
	w, err := openWAL(dir, maxSeg)
	if err != nil {
		return nil, err
	}
	s := &walSpool{
		wal:       w,
		ch:        make(chan leased, capacity),
		capacity:  capacity,
		done:      map[uint64]bool{},
		archive:   archive,
		stopCh:    make(chan struct{}),
		ckptEvery: ckptEvery,
	}
	s.watermark = w.readCheckpoint()

	// Boot replay: everything above the watermark is unpersisted-or-maybe-persisted;
	// replay it (the store's merge + erasure guard make re-delivery idempotent + G3).
	if err := w.replay(s.watermark, func(seq uint64, payload []byte) error {
		j, derr := decodeJob(payload)
		if derr != nil {
			return derr // a corrupt record before the torn tail is a hard error
		}
		s.inFlight.Add(1)
		s.ch <- leased{seq: seq, j: j}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("wal replay: %w", err)
	}

	s.wg.Add(1)
	go s.checkpointLoop()
	return s, nil
}

func (s *walSpool) Append(j job) error {
	// Reserve an in-flight slot first so concurrent Appends can't overshoot capacity;
	// on overflow, release and shed (G2) WITHOUT writing to the WAL.
	if n := s.inFlight.Add(1); n > int64(s.capacity) {
		s.inFlight.Add(-1)
		return ErrSpoolFull
	}
	seq, err := s.wal.append(encodeJob(j))
	if err != nil {
		s.inFlight.Add(-1)
		return err
	}
	// A slot is reserved, and ch capacity == capacity, so this never blocks.
	s.ch <- leased{seq: seq, j: j}
	return nil
}

func (s *walSpool) Next(ctx context.Context) (leased, bool) {
	select {
	case <-ctx.Done():
		return leased{}, false
	case l, ok := <-s.ch:
		return l, ok
	}
}

// Commit marks a leased record durably persisted: release its in-flight slot and
// advance the contiguous watermark. Checkpoint/truncate/archive happen on the
// timer, not per-commit (a lagging watermark only costs extra idempotent replay).
func (s *walSpool) Commit(l leased) {
	s.inFlight.Add(-1)
	if l.seq == 0 {
		return
	}
	s.mu.Lock()
	s.done[l.seq] = true
	for s.done[s.watermark+1] {
		s.watermark++
		delete(s.done, s.watermark)
	}
	s.mu.Unlock()
}

// requeue retries a failed record without a new WAL write (the durable record is
// already on disk). Non-blocking: if the buffer is momentarily full the record
// stays durable and uncommitted, so it replays on the next boot — never lost.
func (s *walSpool) requeue(l leased) {
	select {
	case s.ch <- l:
	default:
	}
}

// deadLetter gives up on a poison record: count it and Commit so the watermark can
// advance past it (a permanently-failing record must not wedge the checkpoint).
func (s *walSpool) deadLetter(l leased) {
	s.deadLettered.Add(1)
	s.Commit(l)
}

func (s *walSpool) Durable() bool       { return true }
func (s *walSpool) Len() int            { return int(s.inFlight.Load()) }
func (s *walSpool) Cap() int            { return s.capacity }
func (s *walSpool) DeadLettered() int64 { return s.deadLettered.Load() }

func (s *walSpool) tryNext() (leased, bool) {
	select {
	case l, ok := <-s.ch:
		return l, ok
	default:
		return leased{}, false
	}
}

func (s *walSpool) checkpointLoop() {
	defer s.wg.Done()
	t := time.NewTicker(s.ckptEvery)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			s.checkpoint()
			return
		case <-t.C:
			s.checkpoint()
		}
	}
}

// checkpoint durably records the watermark, then archives + drops sealed segments
// fully below it. Archival (object-store) happens BEFORE deletion so a disaster
// restore can recover them; if the sink is nil, segments are dropped locally.
func (s *walSpool) checkpoint() {
	s.mu.Lock()
	wm := s.watermark
	s.mu.Unlock()
	if wm == 0 {
		return
	}
	if err := s.wal.writeCheckpoint(wm); err != nil {
		return
	}
	if s.archive != nil {
		s.archiveBelow(wm)
	}
	_, _ = s.wal.truncate(wm)
}

// archiveBelow uploads sealed segments fully below the watermark to the sink; the
// local truncate that follows only removes them once they are archived.
func (s *walSpool) archiveBelow(wm uint64) {
	segs, err := s.wal.segments()
	if err != nil {
		return
	}
	s.wal.mu.Lock()
	active := s.wal.curName
	s.wal.mu.Unlock()
	for i, seg := range segs {
		if seg == active || i+1 >= len(segs) {
			continue
		}
		maxInSeg := segStart(segs[i+1]) - 1
		if maxInSeg > wm {
			continue
		}
		_ = s.archive.Put(context.Background(), seg, s.wal.dir+"/"+seg)
	}
}

func (s *walSpool) Close() error {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
	return s.wal.close()
}

// ---- job wire encoding (WAL payload) ----

// encodeJob serializes a job for the WAL: receivedAt(ns,int64) then length-prefixed
// contentType, bearer, body. It carries no DB handle — exactly the hot-path bytes.
func encodeJob(j job) []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(j.receivedAt.UnixNano()))
	buf = appendBytes(buf, []byte(j.contentType))
	buf = appendBytes(buf, []byte(j.bearer))
	buf = appendBytes(buf, j.body)
	return buf
}

func appendBytes(buf, b []byte) []byte {
	var l [4]byte
	binary.LittleEndian.PutUint32(l[:], uint32(len(b)))
	buf = append(buf, l[:]...)
	return append(buf, b...)
}

func decodeJob(p []byte) (job, error) {
	if len(p) < 8 {
		return job{}, errors.New("short job record")
	}
	ns := int64(binary.LittleEndian.Uint64(p[:8]))
	off := 8
	ct, off, err := readBytes(p, off)
	if err != nil {
		return job{}, err
	}
	bearer, off, err := readBytes(p, off)
	if err != nil {
		return job{}, err
	}
	body, _, err := readBytes(p, off)
	if err != nil {
		return job{}, err
	}
	return job{
		body:        body,
		contentType: string(ct),
		bearer:      string(bearer),
		receivedAt:  time.Unix(0, ns).UTC(),
	}, nil
}

func readBytes(p []byte, off int) ([]byte, int, error) {
	if off+4 > len(p) {
		return nil, off, errors.New("truncated length")
	}
	n := int(binary.LittleEndian.Uint32(p[off : off+4]))
	off += 4
	if n < 0 || off+n > len(p) {
		return nil, off, errors.New("truncated payload")
	}
	return p[off : off+n], off + n, nil
}

func (m *memSpool) Durable() bool { return false }
