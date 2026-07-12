package redisstore_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/m7mdhka/llmobs/kernel/internal/bus/redisstore"
)

// TestSentinelFailoverSelfHeals is the proof: a Sentinel-backed client keeps
// working across a master failover WITHOUT a process restart — it re-resolves the
// promoted master through Sentinel, carries its auth, and retries. The test writes
// an event, forces a real Sentinel failover (promoting the replica), and asserts a
// subsequent write+read on the SAME client succeeds and the pre-failover event
// (replicated to the new master) is still there.
//
// Env-gated on LLMOBS_REDIS_SENTINEL_ADDR (comma-sep) + LLMOBS_REDIS_MASTER_NAME;
// skips otherwise. Must run where the Sentinel-announced node IPs are reachable
// (e.g. inside the topology's docker network).
func TestSentinelFailoverSelfHeals(t *testing.T) {
	addrsEnv := os.Getenv("LLMOBS_REDIS_SENTINEL_ADDR")
	master := os.Getenv("LLMOBS_REDIS_MASTER_NAME")
	if addrsEnv == "" || master == "" {
		t.Skip("set LLMOBS_REDIS_SENTINEL_ADDR + LLMOBS_REDIS_MASTER_NAME to run the Sentinel failover proof")
	}
	addrs := strings.Split(addrsEnv, ",")
	ctx := context.Background()

	rdb, err := redisstore.Dial(ctx, redisstore.Config{MasterName: master, SentinelAddrs: addrs})
	if err != nil {
		t.Fatalf("dial via sentinel: %v", err)
	}
	defer rdb.Close()
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flushdb: %v", err)
	}
	s := redisstore.New(rdb, "fo")

	// Write BEFORE the failover — lands on the current master, replicates to the replica.
	if _, err := s.Append(ctx, "t", "p", "before"); err != nil {
		t.Fatalf("pre-failover append: %v", err)
	}

	// Force a real failover through Sentinel and wait for the master address to change.
	sent := redis.NewSentinelClient(&redis.Options{Addr: addrs[0]})
	defer sent.Close()
	origAddr, err := sent.GetMasterAddrByName(ctx, master).Result()
	if err != nil {
		t.Fatalf("get master addr: %v", err)
	}
	if err := sent.Failover(ctx, master).Err(); err != nil {
		t.Fatalf("trigger sentinel failover: %v", err)
	}
	if !waitMasterChanged(ctx, sent, master, origAddr, 40*time.Second) {
		t.Fatal("sentinel did not promote a new master within the timeout")
	}

	// The SAME client (no re-dial) must self-heal onto the promoted master. go-redis
	// re-resolves + retries; give it a bounded window to converge.
	var appended bool
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := s.Append(ctx, "t", "p", "after"); err == nil {
			appended = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !appended {
		t.Fatal("client did not self-heal onto the promoted master (no restart)")
	}

	// Data continuity: "before" (replicated to the new master) + "after" are both present.
	got, err := s.After(ctx, "t", "p", 0, 10)
	if err != nil {
		t.Fatalf("post-failover read: %v", err)
	}
	subjects := map[string]bool{}
	for _, d := range got {
		subjects[d.SubjectID] = true
	}
	if !subjects["before"] || !subjects["after"] {
		t.Fatalf("expected before+after after failover, got %+v", got)
	}
}

func waitMasterChanged(ctx context.Context, sent *redis.SentinelClient, master string, orig []string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cur, err := sent.GetMasterAddrByName(ctx, master).Result()
		if err == nil && len(cur) == 2 && strings.Join(cur, ":") != strings.Join(orig, ":") {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}
