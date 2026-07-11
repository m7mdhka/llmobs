// Package redisstore is the SCALE event-bus backend: a bus.Store implemented over
// Redis/Valkey Streams (ADR-0028), parallel to the Postgres-lite store. It holds
// ONLY the persistence — the replay / at-least-once / backlog-cap / DLQ logic lives
// in the shared bus.Bus and is NOT reimplemented here. The same bus conformance
// suite (H6) runs green against this backend, proving the contract is the interface.
//
// Offset model: the bus.Store contract is int64 monotonic offsets per (topic,
// project). Redis Stream entry IDs are `ms-seq` strings, so we drive our own int64
// via an INCR counter and write each entry with the explicit id `<n>-0` — the
// counter IS both LatestID and the entry's sort key, so `After` is a plain XRANGE.
package redisstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"

	"github.com/m7mdhka/llmobs/kernel/internal/bus"
)

// Store implements bus.Store over Redis/Valkey Streams.
type Store struct {
	rdb *redis.Client
	ns  string // key namespace prefix (brand-derived)
}

var _ bus.Store = (*Store)(nil)

// New builds a Store over an established client. ns namespaces all keys.
func New(rdb *redis.Client, ns string) *Store {
	if ns == "" {
		ns = "llmobs"
	}
	return &Store{rdb: rdb, ns: ns}
}

func (s *Store) seqKey(topic, project string) string {
	return fmt.Sprintf("%s:seq:{%s|%s}", s.ns, project, topic)
}
func (s *Store) logKey(topic, project string) string {
	return fmt.Sprintf("%s:log:{%s|%s}", s.ns, project, topic)
}
func (s *Store) offKey(pluginID, project, topic string) string {
	return fmt.Sprintf("%s:off:%s|%s|%s", s.ns, pluginID, project, topic)
}
func (s *Store) dlqKey(pluginID, project, topic string) string {
	return fmt.Sprintf("%s:dlq:%s|%s|%s", s.ns, pluginID, project, topic)
}
func (s *Store) dlqCountKey() string { return s.ns + ":dlqcount" }

// appendScript atomically assigns the next int64 id (INCR) and XADDs the entry with
// the explicit id `<n>-0`, so ids are strictly monotonic and the stream order can
// never diverge from the counter under concurrent Appends. KEYS: seq, log.
// ARGV: subject.
var appendScript = redis.NewScript(`
local id = redis.call('INCR', KEYS[1])
redis.call('XADD', KEYS[2], id .. '-0', 'subject', ARGV[1])
return id
`)

func (s *Store) Append(ctx context.Context, topic, projectID, subjectID string) (int64, error) {
	id, err := appendScript.Run(ctx, s.rdb,
		[]string{s.seqKey(topic, projectID), s.logKey(topic, projectID)}, subjectID).Int64()
	if err != nil {
		return 0, fmt.Errorf("redis append: %w", err)
	}
	return id, nil
}

func (s *Store) LatestID(ctx context.Context, topic, projectID string) (int64, error) {
	// The seq counter is the highest id appended (0 if never).
	v, err := s.rdb.Get(ctx, s.seqKey(topic, projectID)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("redis latest id: %w", err)
	}
	return v, nil
}

func (s *Store) After(ctx context.Context, topic, projectID string, afterID int64, limit int) ([]bus.Delivered, error) {
	if limit <= 0 {
		return nil, nil
	}
	// Exclusive start: entries with id > afterID. Our ids are `<n>-0`; `(afterID-0`
	// is exclusive of afterID, so it yields afterID+1 onward. afterID<=0 → from the
	// minimum.
	start := "-"
	if afterID > 0 {
		start = "(" + strconv.FormatInt(afterID, 10) + "-0"
	}
	msgs, err := s.rdb.XRangeN(ctx, s.logKey(topic, projectID), start, "+", int64(limit)).Result()
	if err != nil {
		return nil, fmt.Errorf("redis after: %w", err)
	}
	out := make([]bus.Delivered, 0, len(msgs))
	for _, m := range msgs {
		id, perr := parseEntryID(m.ID)
		if perr != nil {
			return nil, perr
		}
		subject, _ := m.Values["subject"].(string)
		out = append(out, bus.Delivered{ID: id, Topic: topic, ProjectID: projectID, SubjectID: subject})
	}
	return out, nil
}

func (s *Store) Offset(ctx context.Context, pluginID, projectID, topic string) (int64, error) {
	v, err := s.rdb.Get(ctx, s.offKey(pluginID, projectID, topic)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("redis offset: %w", err)
	}
	return v, nil
}

// setOffsetScript advances the offset only when the new id is greater (the
// monotonic GREATEST guarantee the Postgres store enforces in SQL), so a
// concurrent or out-of-order ack can never move an offset backward. KEYS: offset.
// ARGV: id.
var setOffsetScript = redis.NewScript(`
local cur = tonumber(redis.call('GET', KEYS[1]) or '0')
local id = tonumber(ARGV[1])
if id > cur then
  redis.call('SET', KEYS[1], id)
end
return 1
`)

func (s *Store) SetOffset(ctx context.Context, pluginID, projectID, topic string, id int64) error {
	if err := setOffsetScript.Run(ctx, s.rdb,
		[]string{s.offKey(pluginID, projectID, topic)}, id).Err(); err != nil {
		return fmt.Errorf("redis set offset: %w", err)
	}
	return nil
}

func (s *Store) DeadLetter(ctx context.Context, pluginID, projectID, topic string, fromID, toID int64, reason string) error {
	pipe := s.rdb.TxPipeline()
	pipe.XAdd(ctx, &redis.XAddArgs{
		Stream: s.dlqKey(pluginID, projectID, topic),
		Values: map[string]any{
			"from":   fromID,
			"to":     toID,
			"reason": reason,
		},
	})
	pipe.Incr(ctx, s.dlqCountKey())
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis dead-letter: %w", err)
	}
	return nil
}

// DLQLen returns the total number of dead-letter range records (for conformance;
// mirrors MemStore.DLQLen).
func (s *Store) DLQLen(ctx context.Context) (int, error) {
	v, err := s.rdb.Get(ctx, s.dlqCountKey()).Int()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return v, nil
}

// parseEntryID extracts the int64 id from a Stream entry id `<n>-<seq>`.
func parseEntryID(streamID string) (int64, error) {
	ms := streamID
	if i := strings.IndexByte(streamID, '-'); i >= 0 {
		ms = streamID[:i]
	}
	id, err := strconv.ParseInt(ms, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad stream id %q: %w", streamID, err)
	}
	return id, nil
}
