package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func setupKV(t *testing.T) (*PluginKV, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("LLMOBS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set LLMOBS_TEST_DATABASE_URL to run the plugin-kv tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM plugin_kv WHERE plugin_id='p/o6'`)
	return NewPluginKV(pool), pool
}

// TestPluginKVUserScopeIsolation is the O6 store-level proof (real Postgres PK): within the same
// (plugin, project), the user_id dimension isolates per-user rows from each other AND from the
// project-scope row (user_id=''). One user's value is never returned to another.
func TestPluginKVUserScopeIsolation(t *testing.T) {
	kv, _ := setupKV(t)
	ctx := context.Background()
	const p, pr = "p/o6", "proj"

	// Project scope (user_id="") and two users write the SAME key.
	if err := kv.Set(ctx, p, pr, "", "k", json.RawMessage(`"project"`)); err != nil {
		t.Fatal(err)
	}
	if err := kv.Set(ctx, p, pr, "alice@x", "k", json.RawMessage(`"alice"`)); err != nil {
		t.Fatal(err)
	}
	if err := kv.Set(ctx, p, pr, "bob@x", "k", json.RawMessage(`"bob"`)); err != nil {
		t.Fatal(err)
	}
	// Each scope reads back ITS OWN value — three distinct rows for the same key.
	for _, c := range []struct{ user, want string }{{"", `"project"`}, {"alice@x", `"alice"`}, {"bob@x", `"bob"`}} {
		v, found, err := kv.Get(ctx, p, pr, c.user, "k")
		if err != nil || !found || string(v) != c.want {
			t.Fatalf("get user=%q = (%s,%v,%v), want %s", c.user, v, found, err, c.want)
		}
	}
	// Alice's list shows only her keys, not the project row or bob's.
	if err := kv.Set(ctx, p, pr, "alice@x", "only-alice", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	keys, err := kv.List(ctx, p, pr, "alice@x", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 { // "k" and "only-alice"
		t.Fatalf("alice list = %v, want exactly her 2 keys", keys)
	}
	// Deleting alice's key does not touch bob's or the project's.
	if err := kv.Delete(ctx, p, pr, "alice@x", "k"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := kv.Get(ctx, p, pr, "bob@x", "k"); !found {
		t.Fatal("deleting alice's key must not delete bob's")
	}
	if _, found, _ := kv.Get(ctx, p, pr, "", "k"); !found {
		t.Fatal("deleting alice's key must not delete the project row")
	}
}
