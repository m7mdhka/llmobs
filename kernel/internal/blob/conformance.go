package blob

import (
	"bytes"
	"context"
	"io"
	"testing"
)

// RunStoreConformance exercises the behaviors EVERY blob.Store backend must satisfy,
// so the local filesystem adapter and the S3 adapter are held to one contract (the blob
// analogue of the cross-adapter storage/bus conformance). newStore returns a fresh,
// empty store; keys are produced through DeriveKey so the test uses real physical keys.
func RunStoreConformance(t *testing.T, newStore func(t *testing.T) Store) {
	ctx := context.Background()
	key := func(t *testing.T, logical string) string {
		t.Helper()
		k, err := DeriveKey("proj-1", "acme/w", logical)
		if err != nil {
			t.Fatalf("DeriveKey: %v", err)
		}
		return k
	}

	t.Run("RoundTrip", func(t *testing.T) {
		s := newStore(t)
		k := key(t, "round.json")
		payload := []byte(`{"n":1}`)
		if err := s.Put(ctx, k, bytes.NewReader(payload), int64(len(payload)), "application/json"); err != nil {
			t.Fatalf("put: %v", err)
		}
		rc, info, err := s.Get(ctx, k)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer rc.Close()
		got, _ := io.ReadAll(rc)
		if !bytes.Equal(got, payload) {
			t.Fatalf("round-trip mismatch: %q != %q", got, payload)
		}
		if info.Size != int64(len(payload)) {
			t.Fatalf("size mismatch: %d != %d", info.Size, len(payload))
		}
		if info.ContentType != "application/json" {
			t.Fatalf("content-type not preserved: %q", info.ContentType)
		}
	})

	t.Run("GetMissingIsErrNotFound", func(t *testing.T) {
		s := newStore(t)
		if _, _, err := s.Get(ctx, key(t, "absent")); err != ErrNotFound {
			t.Fatalf("missing key must be ErrNotFound, got %v", err)
		}
	})

	t.Run("DeleteIsIdempotent", func(t *testing.T) {
		s := newStore(t)
		k := key(t, "del")
		if err := s.Put(ctx, k, bytes.NewReader([]byte("x")), 1, ""); err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(ctx, k); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if err := s.Delete(ctx, k); err != nil {
			t.Fatalf("second delete must be a no-op: %v", err)
		}
		if _, _, err := s.Get(ctx, k); err != ErrNotFound {
			t.Fatalf("get after delete must be ErrNotFound, got %v", err)
		}
	})

	t.Run("OverwriteReplaces", func(t *testing.T) {
		s := newStore(t)
		k := key(t, "ow")
		if err := s.Put(ctx, k, bytes.NewReader([]byte("old")), 3, ""); err != nil {
			t.Fatal(err)
		}
		if err := s.Put(ctx, k, bytes.NewReader([]byte("newer")), 5, ""); err != nil {
			t.Fatal(err)
		}
		rc, info, err := s.Get(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		got, _ := io.ReadAll(rc)
		if string(got) != "newer" || info.Size != 5 {
			t.Fatalf("overwrite did not replace: %q size=%d", got, info.Size)
		}
	})

	t.Run("DistinctKeysIsolated", func(t *testing.T) {
		s := newStore(t)
		ka, kb := key(t, "a"), key(t, "b")
		if err := s.Put(ctx, ka, bytes.NewReader([]byte("AAA")), 3, ""); err != nil {
			t.Fatal(err)
		}
		if err := s.Put(ctx, kb, bytes.NewReader([]byte("BBB")), 3, ""); err != nil {
			t.Fatal(err)
		}
		rc, _, err := s.Get(ctx, ka)
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		got, _ := io.ReadAll(rc)
		if string(got) != "AAA" {
			t.Fatalf("key a returned another key's bytes: %q", got)
		}
	})
}
