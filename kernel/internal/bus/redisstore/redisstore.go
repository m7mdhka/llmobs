// Package redisstore is the SCALE event-bus backend: a bus.Store implemented over
// Redis/Valkey Streams, parallel to the Postgres-lite store. It holds
// ONLY the persistence — the replay / at-least-once / backlog-cap / DLQ logic lives
// in the shared bus.Bus and is NOT reimplemented here. The same bus conformance
// suite runs green against this backend, proving the contract is the interface.
//
// Offset model: the bus.Store contract is int64 offsets that are the idempotency
// key, so they must be GLOBALLY unique (matching Postgres's table-wide BIGSERIAL).
// Stream entry IDs are `ms-seq` strings, so Append drives a single global INCR
// counter and writes each entry with the explicit id `<n>-0`; ids are globally
// unique and strictly ascending within each per-(topic,project) stream, so `After`
// is a plain XRANGE and `LatestID` is the stream's last entry (XREVRANGE).
//
// Key injectivity: every key component (project, topic, pluginID — topic is
// attacker-controlled) is percent-encoded (enc) so the `:` separator and Redis
// metacharacters can never appear in a component, making the component→key mapping
// injective (no cross-tenant key collision).
//
// Deployment: single-node or Sentinel (HA). Redis Cluster is NOT a target — the
// Append script and the DeadLetter pipeline each touch two keys that would land in
// different hash slots (CROSSSLOT); cluster support is a documented follow-up.
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
	// streamMaxLen bounds each per-(topic,project) stream via an APPROXIMATE XADD
	// MAXLEN so the Streams log cannot grow without bound. It is set to the bus
	// backlog cap: the bus dead-letters any subscriber more than backlogCap behind
	// latest (bus.go), so NOTHING below `latest-backlogCap` is ever deliverable — an
	// approximate trim to ~backlogCap (which Redis keeps AT LEAST, trimming in whole
	// macro-node chunks) therefore preserves the entire deliverable window and only
	// drops entries already skipped/dead-lettered. This is a watermark tied to the
	// delivery contract, NOT a naive fixed MAXLEN that could drop unconsumed backlog.
	// 0 disables trimming (the log grows unbounded — used only where a cap is unset).
	streamMaxLen int64
}

var _ bus.Store = (*Store)(nil)

// New builds a Store over an established client. ns namespaces all keys (the
// caller supplies it, brand-derived — no product name is hardcoded here).
func New(rdb *redis.Client, ns string) *Store {
	return &Store{rdb: rdb, ns: ns}
}

// SetStreamMaxLen sets the approximate per-stream retention. Pass the bus backlog
// cap: entries older than that window are never deliverable, so trimming to it is safe.
// Non-positive disables trimming.
func (s *Store) SetStreamMaxLen(n int64) {
	if n < 0 {
		n = 0
	}
	s.streamMaxLen = n
}

// seqKey is a SINGLE GLOBAL counter, so ids are globally unique across all
// (topic, project) — matching the Postgres store's table-wide BIGSERIAL. Per-topic
// counters would restart at 1 per topic, colliding the `Delivered.ID` idempotency
// key across topics (a plugin subscribed to two topics that dedupes on id alone
// would silently drop events on scale but not lite). The id is the idempotency key;
// it MUST be globally unique.
func (s *Store) seqKey() string { return s.ns + ":seq" }
func (s *Store) logKey(topic, project string) string {
	return s.ns + ":log:" + enc(project) + ":" + enc(topic)
}
func (s *Store) offKey(pluginID, project, topic string) string {
	return s.ns + ":off:" + enc(pluginID) + ":" + enc(project) + ":" + enc(topic)
}
func (s *Store) dlqKey(pluginID, project, topic string) string {
	return s.ns + ":dlq:" + enc(pluginID) + ":" + enc(project) + ":" + enc(topic)
}
func (s *Store) dlqCountKey() string { return s.ns + ":dlqcount" }

// enc percent-encodes every byte of a key component that is not in the safe set
// [A-Za-z0-9._-], so the `:` key separator (and Redis glob/hash-tag metacharacters)
// can NEVER appear in a component. This makes the (project, topic, pluginID) → key
// mapping INJECTIVE: a component containing `:`/`|`/`{`/`}` cannot be confused with
// the delimiter to collide two tenants' keys. `topic` is attacker-controlled (a
// plugin's poll body) and project ids are free-form, so a raw-concatenation scheme
// would be a cross-tenant break; encoding removes the ambiguity regardless of content.
func enc(component string) string {
	var b strings.Builder
	for i := 0; i < len(component); i++ {
		c := component[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '.' || c == '_' || c == '-' {
			b.WriteByte(c)
		} else {
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}

// appendScript atomically assigns the next GLOBAL int64 id (INCR on the single seq
// key) and XADDs the entry to the per-(topic,project) log with the explicit id
// `<n>-0`, so ids are globally unique AND strictly ascending within each stream,
// and stream order can never diverge from the counter under concurrent Appends.
// When ARGV[2] (maxlen) > 0 the XADD carries an APPROXIMATE MAXLEN so the log
// is bounded to the delivery window in the same atomic op — no separate trim pass,
// no drift. KEYS: global-seq, log. ARGV: subject, maxlen.
var appendScript = redis.NewScript(`
local id = redis.call('INCR', KEYS[1])
local maxlen = tonumber(ARGV[2])
if maxlen and maxlen > 0 then
  redis.call('XADD', KEYS[2], 'MAXLEN', '~', maxlen, id .. '-0', 'subject', ARGV[1])
else
  redis.call('XADD', KEYS[2], id .. '-0', 'subject', ARGV[1])
end
return id
`)

func (s *Store) Append(ctx context.Context, topic, projectID, subjectID string) (int64, error) {
	id, err := appendScript.Run(ctx, s.rdb,
		[]string{s.seqKey(), s.logKey(topic, projectID)}, subjectID, s.streamMaxLen).Int64()
	if err != nil {
		return 0, fmt.Errorf("redis append: %w", err)
	}
	return id, nil
}

func (s *Store) LatestID(ctx context.Context, topic, projectID string) (int64, error) {
	// Per-(topic,project) latest = the highest id in THIS topic's stream (the global
	// counter is not per-topic). XREVRANGE + - COUNT 1 is the last entry, or 0.
	msgs, err := s.rdb.XRevRangeN(ctx, s.logKey(topic, projectID), "+", "-", 1).Result()
	if err != nil {
		return 0, fmt.Errorf("redis latest id: %w", err)
	}
	if len(msgs) == 0 {
		return 0, nil
	}
	return parseEntryID(msgs[0].ID)
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

// parseEntryID extracts the int64 id from a Stream entry id `<n>-<seq>`. This
// backend writes every entry id as `<n>-0`, so a parse failure is a backend
// invariant violation, not attacker-reachable input.
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
