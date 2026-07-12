package pluginapi

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"net/http"
)

// KernelKey publishes the kernel's Ed25519 PUBLIC key so a plugin backend — in any
// language — can verify kernel-signed identity assertions. The handshake goes
// kernel→plugin, so the plugin has no other way to obtain the key; a Go plugin hid
// this by receiving the key in-process, but a cross-language backend needs it
// published. This is the minimal single-key form; a JWKS endpoint with key rotation
// is a deferred hardening pass. The public key is public — this endpoint is
// unauthenticated by design.
type KernelKey struct {
	pub ed25519.PublicKey
}

func NewKernelKey(pub ed25519.PublicKey) *KernelKey { return &KernelKey{pub: pub} }

// Register mounts GET {path} (e.g. /v1alpha1/plugin/kernel-key).
func (h *KernelKey) Register(mux *http.ServeMux, path string) {
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"algorithm": "ed25519",
			// Both encodings so any language decodes trivially. NOTE: token payload/
			// signature segments are UNPADDED base64url (Go RawURLEncoding) — decoders
			// that require padding (e.g. Python) must re-pad. The key here is a full
			// 32-byte value shown in hex + standard base64.
			"public_key_hex":    hex.EncodeToString(h.pub),
			"public_key_base64": base64.StdEncoding.EncodeToString(h.pub),
		})
	})
}
