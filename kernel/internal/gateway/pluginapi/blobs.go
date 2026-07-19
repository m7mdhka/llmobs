package pluginapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/m7mdhka/llmobs/kernel/internal/blob"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginauth"
)

// BlobStore is the large-artifact surface the blobs endpoints need (interface-at-consumer).
// It takes a PHYSICAL key — the endpoints derive it from the caller's (plugin, project) via
// blob.DeriveKey, so a plugin can only ever address its own objects.
type BlobStore interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, blob.ObjectInfo, error)
	Delete(ctx context.Context, key string) error
}

// Blobs serves the `blobs` primitive (kernel-brokered large-artifact storage), gated on the
// blobs capability. Every object is scoped to the caller's (plugin_id, project_id): the
// endpoints turn the plugin-supplied logical key into a tenant-scoped physical key at the
// ONE blob.DeriveKey seam, so cross-tenant/cross-plugin access is impossible by construction
// and the plugin never touches the object store.
type Blobs struct {
	authz    *pluginauth.Authorizer
	store    BlobStore
	maxBytes int64
}

func NewBlobs(authz *pluginauth.Authorizer, store BlobStore, maxBytes int64) *Blobs {
	if maxBytes <= 0 {
		maxBytes = 64 << 20 // 64 MiB default upload ceiling
	}
	return &Blobs{authz: authz, store: store, maxBytes: maxBytes}
}

func (h *Blobs) Register(mux *http.ServeMux, prefix string) {
	mux.HandleFunc(prefix+"/put", h.handle(h.put))
	mux.HandleFunc(prefix+"/get", h.handle(h.get))
	mux.HandleFunc(prefix+"/delete", h.handle(h.del))
}

func (h *Blobs) handle(fn func(http.ResponseWriter, *http.Request, pluginauth.Caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		caller, status, err := h.authz.Require(r, "blobs")
		if err != nil {
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		fn(w, r, caller)
	}
}

// key derives the tenant-scoped physical key from the caller and the plugin-supplied
// logical key (the `key` query param). A malformed logical key is a 400.
func (h *Blobs) key(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) (string, bool) {
	logical := r.URL.Query().Get("key")
	phys, err := blob.DeriveKey(c.ProjectID, c.PluginID, logical)
	if err != nil {
		var ve *blob.ValidationError
		if errors.As(err, &ve) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": ve.Error()})
			return "", false
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid key"})
		return "", false
	}
	return phys, true
}

func (h *Blobs) put(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	phys, ok := h.key(w, r, c)
	if !ok {
		return
	}
	contentType := r.Header.Get("Content-Type")
	// Bound the upload: a plugin must not stream an unbounded body into the object store.
	// MaxBytesReader makes an over-cap body fail the read. The overCapReader wrapper records
	// whether that limit was hit, so an oversize upload returns 413 IDENTICALLY on both
	// adapters — the local adapter propagates the *http.MaxBytesError, but the S3 client may
	// wrap/replace the reader error with its own, losing the chain, so we can't rely on
	// errors.As alone.
	oc := &overCapReader{r: http.MaxBytesReader(w, r.Body, h.maxBytes)}
	// size -1: the body is streamed (Content-Length may be absent/untrusted); both adapters
	// accept an unknown size, and the cap above is the real bound.
	err := h.store.Put(r.Context(), phys, oc, -1, contentType)
	if oc.overCap {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "blob exceeds size limit"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "put failed"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// overCapReader wraps a MaxBytesReader and remembers whether the size limit was exceeded,
// so the handler can return a consistent 413 regardless of whether the storage backend
// preserved the underlying *http.MaxBytesError in its own error.
type overCapReader struct {
	r       io.Reader
	overCap bool
}

func (o *overCapReader) Read(p []byte) (int, error) {
	n, err := o.r.Read(p)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			o.overCap = true
		}
	}
	return n, err
}

func (h *Blobs) get(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	phys, ok := h.key(w, r, c)
	if !ok {
		return
	}
	rc, info, err := h.store.Get(r.Context(), phys)
	if errors.Is(err, blob.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "get failed"})
		return
	}
	defer rc.Close()
	if info.ContentType != "" {
		w.Header().Set("Content-Type", info.ContentType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	if info.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc) // header already sent; a mid-stream error can only truncate
}

func (h *Blobs) del(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	phys, ok := h.key(w, r, c)
	if !ok {
		return
	}
	if err := h.store.Delete(r.Context(), phys); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "delete failed"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
