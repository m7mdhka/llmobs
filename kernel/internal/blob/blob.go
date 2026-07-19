// Package blob is the kernel-brokered large-artifact seam: a small, backend-agnostic
// Store interface plus the ONE tenant-scoped, filesystem-safe, injective key derivation
// that every path funnels through. It is the storage-side of the future `blobs` plugin
// primitive (a plugin's exported reports, model files, big attachments) — the analogue of
// the bus.Store / storage.Store seams, so a backend implements only persistence while the
// kernel owns scoping and key safety.
//
// The seam is deliberately dumb about tenancy: a Store never sees a project or plugin id.
// Callers MUST turn a plugin-supplied logical key into a physical key through DeriveKey,
// which folds in the SERVER-derived (project, plugin) from the assertion. Because that is
// the only way to mint a physical key, a plugin cannot address — read, overwrite, or
// delete — another tenant's or another plugin's objects by construction, enforced at this
// one convergence seam rather than re-checked per backend.
package blob

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
)

// ObjectInfo is the metadata a Get returns alongside the bytes.
type ObjectInfo struct {
	Key         string // the physical key
	Size        int64
	ContentType string
	ModifiedAt  time.Time
}

// Store is a blob backend (interface-at-consumer). Adapters: a local filesystem store
// (lite / dev / airgap) and, later, an S3-compatible store (scale). The key passed to
// every method is ALWAYS a physical key produced by DeriveKey — an adapter never derives
// or validates tenancy, so the isolation guarantee cannot drift per backend.
type Store interface {
	// Put writes size bytes from r under key, overwriting any existing object.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Get opens the object for reading; the caller closes the reader. Returns ErrNotFound
	// if key does not exist.
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	// Delete removes the object; deleting a missing key is not an error (idempotent).
	Delete(ctx context.Context, key string) error
}

// ErrNotFound is returned by Get for a key that does not exist.
var ErrNotFound = errors.New("blob: object not found")

// Key-safety bounds. maxLogicalKeyLen bounds the RAW plugin-supplied key to give a
// friendly early rejection (its percent-encoded form is worst-case 3× and stays well
// under a segment: 80 * 3 = 240 < 255). maxSegmentBytes is the hard guarantee enforced on
// EVERY encoded segment — including the server-derived project/plugin ids — so a segment
// can never exceed NAME_MAX (255 on ext4/APFS/xfs) regardless of an id's length or
// charset. Bounding the ENCODED segment (not the raw input) is what makes the guarantee
// hold even if an id is ever a long slug rather than a fixed-width uuid.
const (
	maxLogicalKeyLen = 80
	maxSegmentBytes  = 255
)

// ValidationError is a client (400) error: a malformed logical key.
type ValidationError struct{ Reason string }

func (e *ValidationError) Error() string { return "blob: invalid key: " + e.Reason }

// DeriveKey turns a plugin-supplied logical key into the physical key a Store addresses,
// scoped to the SERVER-derived (projectID, pluginID). It is the single tenant-isolation
// seam: the physical layout is  b/<enc(project)>/<enc(plugin)>/<enc(logicalKey)>, three
// path segments, each percent-encoded so it is:
//
//   - injective — enc is injective and encodes any '/' in a component to %2F, so no
//     component can forge a segment boundary and no two distinct (project, plugin, key)
//     triples can ever collide into the same physical key (no cross-tenant aliasing);
//   - filesystem-safe (#92) — every segment is bounded well under NAME_MAX, contains only
//     [A-Za-z0-9._-] plus %HH escapes, and can never be ".", "..", or contain a NUL, so
//     there is no path traversal and no reserved-name hazard;
//   - object-store-safe (#101) — the same encoded key is a valid S3/GCS/Azure object key,
//     so one derivation serves every adapter and the '/' delimiters map to key prefixes.
//
// The logical key is validated first (non-empty, raw-length-bounded, no NUL); then EVERY
// encoded segment (project, plugin, key) is checked against maxSegmentBytes so no segment
// can exceed NAME_MAX whatever an id contains.
func DeriveKey(projectID, pluginID, logicalKey string) (string, error) {
	if logicalKey == "" {
		return "", &ValidationError{Reason: "empty key"}
	}
	if len(logicalKey) > maxLogicalKeyLen {
		return "", &ValidationError{Reason: "key too long"}
	}
	if strings.IndexByte(logicalKey, 0) >= 0 {
		return "", &ValidationError{Reason: "key contains NUL"}
	}
	if projectID == "" || pluginID == "" {
		return "", &ValidationError{Reason: "invalid tenant scope"}
	}
	proj, plug, lk := enc(projectID), enc(pluginID), enc(logicalKey)
	for _, seg := range [...]string{proj, plug, lk} {
		if len(seg) > maxSegmentBytes {
			// A server-derived id (or the key) encodes past the filesystem name limit —
			// a clean 400/validation error, never a raw ENAMETOOLONG from the adapter.
			return "", &ValidationError{Reason: "encoded segment exceeds the filesystem name limit"}
		}
	}
	return "b/" + proj + "/" + plug + "/" + lk, nil
}

// enc percent-encodes every byte outside the safe set [A-Za-z0-9_-], mirroring the bus's
// key encoder but ALSO encoding '.' (the bus keeps it). It is injective, so distinct
// inputs never collide, and it neutralizes '/', '.', control bytes, and NUL — the
// characters that would otherwise forge a path segment or a traversal. Because '.' becomes
// %2E, an encoded segment can NEVER be exactly "." or ".." (those forms require literal
// dots), so path traversal is impossible by construction, no separate reserved-name check
// needed.
func enc(component string) string {
	var b strings.Builder
	for i := 0; i < len(component); i++ {
		c := component[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '_' || c == '-' {
			b.WriteByte(c)
			continue
		}
		const hex = "0123456789ABCDEF"
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}
