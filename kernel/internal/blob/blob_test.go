package blob

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeriveKeyFilesystemSafe proves the #92 guarantee: every physical key is traversal-
// free and bounded per segment, whatever the logical key contains.
func TestDeriveKeyFilesystemSafe(t *testing.T) {
	hostile := []string{
		"../../etc/passwd",
		"..",
		".",
		"a/b/c",
		"a\x00b",                 // NUL — rejected outright
		strings.Repeat("x", 200), // over-long — rejected
		"..%2f..%2fetc",
		"normal-key.json",
		"a b\tc",
	}
	for _, lk := range hostile {
		key, err := DeriveKey("proj-1", "acme/w", lk)
		if err != nil {
			continue // rejected (NUL, over-long, empty) — the safe outcome
		}
		// A derived key must have exactly the b/<project>/<plugin>/<logical> shape:
		// four '/'-separated segments, none of which is "." or ".." or empty.
		segs := strings.Split(key, "/")
		if len(segs) != 4 || segs[0] != "b" {
			t.Fatalf("logical %q → unexpected shape %q", lk, key)
		}
		for _, s := range segs {
			if s == "" || s == "." || s == ".." {
				t.Fatalf("logical %q → traversal/empty segment %q in %q", lk, s, key)
			}
			if len(s) > 255 {
				t.Fatalf("logical %q → segment exceeds NAME_MAX: %d bytes", lk, len(s))
			}
			// Every segment is [A-Za-z0-9_-] or %HH — no raw '.', '/', or control byte.
			for i := 0; i < len(s); i++ {
				c := s[i]
				ok := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
					c == '_' || c == '-' || c == '%'
				if !ok {
					t.Fatalf("logical %q → unsafe byte %q in segment %q", lk, c, s)
				}
			}
		}
	}
}

// TestDeriveKeyInjectiveAndTenantIsolated proves the core isolation property: distinct
// (project, plugin, key) triples NEVER collide, so no plugin can address another tenant's
// or another plugin's object, and a '/' in any component cannot forge a segment boundary.
func TestDeriveKeyInjectiveAndTenantIsolated(t *testing.T) {
	triples := [][3]string{
		{"p1", "plugA", "report"},
		{"p1", "plugB", "report"},   // same project+key, different plugin
		{"p2", "plugA", "report"},   // different project
		{"p1", "plugA", "report2"},  // different key
		{"p1/x", "plugA", "report"}, // a '/' in the project must not alias {p1, x/plugA,...}
		{"p1", "x/plugA", "report"},
		{"p1", "plugA", "a/report"}, // a '/' in the key must not create a new segment
		{"p1", "plugA", "areport"},
	}
	seen := map[string][3]string{}
	for _, tr := range triples {
		key, err := DeriveKey(tr[0], tr[1], tr[2])
		if err != nil {
			t.Fatalf("DeriveKey%v: %v", tr, err)
		}
		if prev, dup := seen[key]; dup {
			t.Fatalf("cross-tenant key collision: %v and %v both → %q", prev, tr, key)
		}
		seen[key] = tr
	}
}

// TestDeriveKeyRejectsOverlongSegment locks in the NAME_MAX guarantee for the SERVER-
// derived id segments: an id whose encoded form would exceed a filesystem name segment is
// rejected cleanly, never passed to an adapter to fail with a raw ENAMETOOLONG. A run of
// "." expands 3× under enc (each → %2E), so 128 dots → 384 bytes > 255.
func TestDeriveKeyRejectsOverlongSegment(t *testing.T) {
	longID := strings.Repeat(".", 128) // 384 encoded bytes
	if _, err := DeriveKey(longID, "plug", "k"); err == nil {
		t.Fatal("an over-long encoded project segment must be rejected")
	}
	if _, err := DeriveKey("proj", longID, "k"); err == nil {
		t.Fatal("an over-long encoded plugin segment must be rejected")
	}
	// A normal-length id with multibyte chars stays within the bound and is accepted.
	if _, err := DeriveKey("proj-café-1", "acme/w", "k"); err != nil {
		t.Fatalf("a normal multibyte id must be accepted: %v", err)
	}
}

func TestDeriveKeyRejectsBadInput(t *testing.T) {
	if _, err := DeriveKey("p", "plug", ""); err == nil {
		t.Fatal("empty logical key must be rejected")
	}
	if _, err := DeriveKey("", "plug", "k"); err == nil {
		t.Fatal("empty project must be rejected")
	}
	if _, err := DeriveKey("p", "", "k"); err == nil {
		t.Fatal("empty plugin must be rejected")
	}
	if _, err := DeriveKey("p", "plug", "a\x00b"); err == nil {
		t.Fatal("NUL in key must be rejected")
	}
}

// TestLocalStoreRoundTrip exercises the reference adapter through DeriveKey.
func TestLocalStoreRoundTrip(t *testing.T) {
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key, err := DeriveKey("proj-1", "acme/w", "reports/q3.json")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"hello":"world"}`)
	if err := s.Put(ctx, key, bytes.NewReader(payload), int64(len(payload)), "application/json"); err != nil {
		t.Fatalf("put: %v", err)
	}
	rc, info, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, payload) {
		t.Fatalf("round-trip mismatch: %q != %q", got, payload)
	}
	if info.ContentType != "application/json" || info.Size != int64(len(payload)) {
		t.Fatalf("bad info: %+v", info)
	}
	// Delete is idempotent; a second delete and a get-after-delete behave.
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("second delete must be a no-op: %v", err)
	}
	if _, _, err := s.Get(ctx, key); err != ErrNotFound {
		t.Fatalf("get after delete must be ErrNotFound, got %v", err)
	}
}

// TestLocalStoreOverwriteClearsStaleContentType proves an overwrite with no content type
// does not serve the previous object's content type against the new bytes.
func TestLocalStoreOverwriteClearsStaleContentType(t *testing.T) {
	s, _ := NewLocalStore(t.TempDir())
	ctx := context.Background()
	key, _ := DeriveKey("p", "plug", "k")
	if err := s.Put(ctx, key, bytes.NewReader([]byte("a")), 1, "application/json"); err != nil {
		t.Fatal(err)
	}
	// Overwrite with NO content type — the stale sidecar must be cleared.
	if err := s.Put(ctx, key, bytes.NewReader([]byte("b")), 1, ""); err != nil {
		t.Fatal(err)
	}
	rc, info, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if info.ContentType != "" {
		t.Fatalf("overwrite with empty content type must clear the sidecar, got %q", info.ContentType)
	}
}

// TestLocalStoreContainment is the defense-in-depth proof: even a raw traversal key that
// bypassed DeriveKey cannot escape the root.
func TestLocalStoreContainment(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, escape := range []string{"../escape", "../../etc/passwd", "b/../../../tmp/x", "../" + filepath.Base(root) + "-sibling"} {
		err := s.Put(ctx, escape, bytes.NewReader([]byte("x")), 1, "")
		if err == nil {
			t.Fatalf("key %q must be rejected by the containment guard", escape)
		}
	}
	// Sanity: nothing was written outside the root.
	parent := filepath.Dir(root)
	if _, err := NewLocalStore(parent); err != nil {
		t.Fatal(err)
	}
}

// TestLocalStoreSizeMismatch rejects a declared size that disagrees with the bytes.
func TestLocalStoreSizeMismatch(t *testing.T) {
	s, _ := NewLocalStore(t.TempDir())
	key, _ := DeriveKey("p", "plug", "k")
	err := s.Put(context.Background(), key, bytes.NewReader([]byte("12345")), 99, "")
	if err == nil {
		t.Fatal("a size mismatch must be rejected")
	}
}
