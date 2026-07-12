package ingest

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// wal is an append-only, fsync-on-append write-ahead log used as the durable
// floor for the ingest spool. Records carry a monotonic seq and a
// per-record CRC; a torn tail (a partial final write after a crash) is detected on
// replay by a short read or CRC mismatch and truncated. The log is segmented so a
// sealed segment fully below the checkpoint watermark can be archived and dropped
// whole.
//
// On-disk record layout (little-endian):
//
//	seq:uint64 | payloadLen:uint32 | payload:[payloadLen]byte | crc32:uint32
//
// crc32 (IEEE) covers seq||payloadLen||payload. The checkpoint file holds the
// highest contiguously-persisted seq: on boot every record with seq > watermark
// replays (idempotent merge makes duplicates safe).
type wal struct {
	dir       string
	maxSeg    int64
	mu        sync.Mutex
	cur       *os.File
	curName   string
	curSeq    uint64 // first seq written to the current segment (for naming)
	curSize   int64
	nextSeq   uint64
	closeOnce sync.Once
}

const (
	walRecordHeader = 12 // seq(8) + payloadLen(4)
	walRecordCRC    = 4
	defaultMaxSeg   = 64 << 20 // 64 MiB segments
	segPrefix       = "seg-"
	segSuffix       = ".wal"
	checkpointFile  = "checkpoint"
)

var crcTable = crc32.MakeTable(crc32.IEEE)

// openWAL opens (creating if needed) a WAL rooted at dir, recovering nextSeq from
// the highest seq already on disk so a restart continues the sequence.
func openWAL(dir string, maxSeg int64) (*wal, error) {
	if maxSeg <= 0 {
		maxSeg = defaultMaxSeg
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("wal mkdir: %w", err)
	}
	// The WAL holds unredacted bodies + bearer tokens; MkdirAll does not tighten a
	// pre-existing dir, so fail closed if it is group/other-accessible rather than
	// silently write PII + replayable credentials to a world-readable path.
	if info, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("wal stat: %w", err)
	} else if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("wal dir %s must be owner-only (0700); found %o — it stores unredacted payloads and bearer tokens", dir, info.Mode().Perm())
	}
	w := &wal{dir: dir, maxSeg: maxSeg, nextSeq: 1}
	// Recover the highest seq across existing segments so we never reuse one.
	segs, err := w.segments()
	if err != nil {
		return nil, err
	}
	for _, seg := range segs {
		_ = replaySegment(filepath.Join(dir, seg), func(seq uint64, _ []byte) error {
			if seq >= w.nextSeq {
				w.nextSeq = seq + 1
			}
			return nil
		})
	}
	return w, nil
}

// append writes one record durably (fsync before return) and returns its seq.
func (w *wal) append(payload []byte) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.cur == nil || w.curSize >= w.maxSeg {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	seq := w.nextSeq
	rec := make([]byte, walRecordHeader+len(payload)+walRecordCRC)
	binary.LittleEndian.PutUint64(rec[0:8], seq)
	binary.LittleEndian.PutUint32(rec[8:12], uint32(len(payload)))
	copy(rec[walRecordHeader:], payload)
	crc := crc32.Checksum(rec[:walRecordHeader+len(payload)], crcTable)
	binary.LittleEndian.PutUint32(rec[walRecordHeader+len(payload):], crc)

	if _, err := w.cur.Write(rec); err != nil {
		return 0, fmt.Errorf("wal write: %w", err)
	}
	if err := w.cur.Sync(); err != nil {
		return 0, fmt.Errorf("wal fsync: %w", err)
	}
	w.curSize += int64(len(rec))
	w.nextSeq++
	return seq, nil
}

// rotate seals the current segment and opens a fresh one named for its first seq.
func (w *wal) rotate() error {
	if w.cur != nil {
		_ = w.cur.Close()
	}
	name := fmt.Sprintf("%s%020d%s", segPrefix, w.nextSeq, segSuffix)
	f, err := os.OpenFile(filepath.Join(w.dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("wal rotate: %w", err)
	}
	w.cur = f
	w.curName = name
	w.curSeq = w.nextSeq
	w.curSize = 0
	return nil
}

// segments returns the segment file names in seq order.
func (w *wal) segments() ([]string, error) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return nil, err
	}
	var segs []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, segPrefix) && strings.HasSuffix(n, segSuffix) {
			segs = append(segs, n)
		}
	}
	sort.Strings(segs) // zero-padded seq in the name → lexical == numeric order
	return segs, nil
}

// replay invokes fn for every record with seq > fromSeq, in seq order. A torn tail
// in any segment stops that segment cleanly (the record was never durably acked).
func (w *wal) replay(fromSeq uint64, fn func(seq uint64, payload []byte) error) error {
	segs, err := w.segments()
	if err != nil {
		return err
	}
	for _, seg := range segs {
		if err := replaySegment(filepath.Join(w.dir, seg), func(seq uint64, payload []byte) error {
			if seq <= fromSeq {
				return nil
			}
			return fn(seq, payload)
		}); err != nil {
			return err
		}
	}
	return nil
}

// replaySegment reads records until EOF or the first torn/corrupt record.
func replaySegment(path string, fn func(seq uint64, payload []byte) error) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	off := 0
	for off+walRecordHeader+walRecordCRC <= len(data) {
		seq := binary.LittleEndian.Uint64(data[off : off+8])
		plen := int(binary.LittleEndian.Uint32(data[off+8 : off+12]))
		end := off + walRecordHeader + plen + walRecordCRC
		if plen < 0 || end > len(data) {
			break // torn tail: partial final record
		}
		payload := data[off+walRecordHeader : off+walRecordHeader+plen]
		gotCRC := binary.LittleEndian.Uint32(data[off+walRecordHeader+plen : end])
		if crc32.Checksum(data[off:off+walRecordHeader+plen], crcTable) != gotCRC {
			break // corrupt tail
		}
		if err := fn(seq, payload); err != nil {
			return err
		}
		off = end
	}
	return nil
}

// writeCheckpoint durably records the highest contiguously-persisted seq.
func (w *wal) writeCheckpoint(seq uint64) error {
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint64(buf[:8], seq)
	binary.LittleEndian.PutUint32(buf[8:], crc32.Checksum(buf[:8], crcTable))
	tmp := filepath.Join(w.dir, checkpointFile+".tmp")
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return err
	}
	// Rename is atomic; a crash leaves either the old or the new checkpoint, never a
	// torn one. A missing/corrupt checkpoint reads as 0 (replay everything — safe).
	return os.Rename(tmp, filepath.Join(w.dir, checkpointFile))
}

func (w *wal) readCheckpoint() uint64 {
	buf, err := os.ReadFile(filepath.Join(w.dir, checkpointFile))
	if err != nil || len(buf) != 12 {
		return 0
	}
	if crc32.Checksum(buf[:8], crcTable) != binary.LittleEndian.Uint32(buf[8:]) {
		return 0
	}
	return binary.LittleEndian.Uint64(buf[:8])
}

// removeSegment deletes a sealed segment by name (never the active one — the
// caller guarantees that). A missing file is not an error (idempotent reap).
func (w *wal) removeSegment(name string) error {
	if err := os.Remove(filepath.Join(w.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// segStart parses the first seq encoded in a segment file name.
func segStart(name string) uint64 {
	s := strings.TrimSuffix(strings.TrimPrefix(name, segPrefix), segSuffix)
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (w *wal) close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var err error
	w.closeOnce.Do(func() {
		if w.cur != nil {
			err = w.cur.Close()
		}
	})
	return err
}
