package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	chgo "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/clickhouse"
)

// TestG3ForgedReplayAfterErase is the load-bearing durability proof: a GDPR-erased span
// re-delivered and sitting in the WAL uncommitted across a crash must NOT resurrect
// on replay. Erasure has been the bug site in two consecutive prior fixes (a timestamp
// bypass, and erase-skipping-soft-deleted), so this gets the full prove-the-negative
// treatment against a REAL store — the WAL replay path funnels through the same
// server-time suppression guard, never around it.
//
// Env-gated on LLMOBS_CH_TEST_DSN (a real ClickHouse); skips otherwise.
func TestG3ForgedReplayAfterErase(t *testing.T) {
	dsn := os.Getenv("LLMOBS_CH_TEST_DSN")
	if dsn == "" {
		t.Skip("set LLMOBS_CH_TEST_DSN to run the G3 forged-replay-after-erase proof")
	}
	ctx := context.Background()

	opts, err := chgo.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := chgo.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, tbl := range []string{"spans", "scores", "erasure_suppression", "erasure_audit", "schema_migrations"} {
		_ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+tbl)
	}
	if err := clickhouse.Migrate(ctx, conn, clickhouse.Config{}); err != nil {
		t.Fatal(err)
	}
	store := clickhouse.NewStore(conn)
	store.SetReadLimits(clickhouse.ReadLimits{
		MaxExecutionTime: 30 * time.Second, MaxMemoryUsage: 1 << 30,
		MaxRowsToRead: 10_000_000, MaxBytesToRead: 1 << 30,
	})

	const pid, spanID, userID = "p", "victim-span", "user-to-erase"
	start := time.Now().UTC()
	payload := map[string]any{
		"project_id": pid, "id": spanID, "trace_id": "t1", "kind": "generation",
		"name": "secret prompt", "user_id": userID,
		"start_time": start.Format(time.RFC3339Nano),
	}

	// 1) Ingest + persist the span.
	if err := store.PersistSpan(ctx, spanEvent(payload, start)); err != nil {
		t.Fatalf("initial persist: %v", err)
	}
	if got, _ := store.GetSpan(ctx, pid, spanID); got == nil {
		t.Fatal("span should exist after persist")
	}

	// 2) GDPR-erase the user (writes a suppression tombstone + physically removes).
	n, _, err := store.EraseSpans(ctx, pid, userID, "dpo@x", start.Add(-time.Hour), start.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("erase: n=%d err=%v", n, err)
	}
	if got, _ := store.GetSpan(ctx, pid, spanID); got != nil {
		t.Fatal("span must be gone after erasure")
	}

	// 3) Forge a re-delivery of the erased span into a durable WAL, and CRASH before
	// it is persisted (Append, never Commit; close before the checkpoint advances).
	dir := filepath.Join(t.TempDir(), "wal")
	body, _ := json.Marshal(payload)
	forged := job{body: body, contentType: "application/json", bearer: "k", receivedAt: time.Now()}
	s1, err := newWALSpool(dir, 16, 0, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Append(forged); err != nil { // durable, uncommitted
		t.Fatal(err)
	}
	_ = s1.Close() // "crash" — the record is on disk, unpersisted

	// 4) Reboot: reopen the WAL (replay) and drive the replayed record through the
	// SAME persist path a live delivery uses.
	s2, err := newWALSpool(dir, 16, 0, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	waitReplay(s2)
	l, ok := s2.tryNext()
	if !ok {
		t.Fatal("the uncommitted erased record must replay from the WAL")
	}

	var p map[string]any
	if err := json.Unmarshal(l.j.body, &p); err != nil {
		t.Fatal(err)
	}
	// The store guard evaluates the tombstone against SERVER time (now64), never any
	// record-carried timestamp — so replay is suppressed exactly as a live re-delivery.
	perr := store.PersistSpan(ctx, spanEvent(p, start))
	if !errors.Is(perr, storage.ErrSuppressedByErasure) {
		t.Fatalf("replayed erased span must be suppressed, got %v", perr)
	}

	// 5) The negative that matters: the span did NOT resurrect.
	if got, _ := store.GetSpan(ctx, pid, spanID); got != nil {
		t.Fatalf("GDPR-erased span resurrected via WAL replay: %s", got)
	}
}

func spanEvent(payload map[string]any, ts time.Time) storage.Event {
	return storage.Event{Op: storage.OpUpsert, EventTS: ts, EventID: "e", Payload: payload}
}
