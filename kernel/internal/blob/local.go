package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LocalStore is the filesystem blob backend: the lite-profile / dev / airgap option (the
// analogue of Postgres-lite for storage). It stores each object as a file under a root
// directory, addressed by the physical key from DeriveKey. A scale deployment swaps in an
// S3-compatible Store behind the same interface; nothing else changes.
type LocalStore struct {
	root string
}

var _ Store = (*LocalStore)(nil)

// NewLocalStore roots a filesystem store at dir (created if missing).
func NewLocalStore(dir string) (*LocalStore, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("blob local root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("blob local mkdir: %w", err)
	}
	return &LocalStore{root: abs}, nil
}

// resolve maps a physical key to an absolute path and PROVES it stays within the root.
// The key from DeriveKey is already traversal-free (dots and slashes inside a component
// are percent-encoded), but this is the defense-in-depth backstop that makes containment
// hold even if a raw key ever reached here: after Clean, the path must be the root itself
// or a descendant, else it is rejected. A Store never trusts its key blindly.
func (s *LocalStore) resolve(key string) (string, error) {
	if key == "" || strings.IndexByte(key, 0) >= 0 {
		return "", &ValidationError{Reason: "empty or NUL key"}
	}
	p := filepath.Clean(filepath.Join(s.root, filepath.FromSlash(key)))
	if p != s.root && !strings.HasPrefix(p, s.root+string(os.PathSeparator)) {
		return "", &ValidationError{Reason: "key escapes the blob root"}
	}
	return p, nil
}

func (s *LocalStore) Put(_ context.Context, key string, r io.Reader, size int64, contentType string) error {
	p, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return fmt.Errorf("blob put mkdir: %w", err)
	}
	// Write to a temp file in the same directory, then rename — an interrupted Put never
	// leaves a half-written object under the real key.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".put-*")
	if err != nil {
		return fmt.Errorf("blob put temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	n, cErr := io.Copy(tmp, r)
	if cErr != nil {
		tmp.Close()
		return fmt.Errorf("blob put copy: %w", cErr)
	}
	if size >= 0 && n != size {
		tmp.Close()
		return &ValidationError{Reason: fmt.Sprintf("declared size %d != written %d", size, n)}
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("blob put close: %w", err)
	}
	// Publish the object atomically FIRST, then reconcile the content-type sidecar so it
	// always matches the current object: write it when a type is given, and CLEAR any
	// stale sidecar from a prior typed write when it is not — otherwise an overwrite with
	// no content type would serve the previous object's type against the new bytes.
	if err := os.Rename(tmpName, p); err != nil {
		return fmt.Errorf("blob put rename: %w", err)
	}
	if contentType != "" {
		if err := os.WriteFile(p+ctSuffix, []byte(contentType), 0o640); err != nil {
			return fmt.Errorf("blob put content-type: %w", err)
		}
	} else {
		_ = os.Remove(p + ctSuffix) // clear a stale sidecar; absent is fine
	}
	return nil
}

func (s *LocalStore) Get(_ context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	p, err := s.resolve(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("blob get open: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, ObjectInfo{}, fmt.Errorf("blob get stat: %w", err)
	}
	ct := ""
	if b, err := os.ReadFile(p + ctSuffix); err == nil {
		ct = string(b)
	}
	return f, ObjectInfo{Key: key, Size: fi.Size(), ContentType: ct, ModifiedAt: fi.ModTime()}, nil
}

func (s *LocalStore) Delete(_ context.Context, key string) error {
	p, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blob delete: %w", err)
	}
	_ = os.Remove(p + ctSuffix) // best-effort sidecar cleanup
	return nil
}

// ctSuffix names the content-type sidecar file for an object. It ends in '!', which is
// outside the encoded key charset ([A-Za-z0-9_-] plus %HH), so it can never collide with
// a real object key.
const ctSuffix = ".ct!"
