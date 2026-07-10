package pluginapi

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/pipeline"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginauth"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

const maxIngestBytes = 4 << 20

// Ingester is the pipeline surface the plugin ingest endpoint needs.
type Ingester interface {
	RunPreauth(ctx context.Context, ing *pipeline.Ingestion) error
}

// Ingest serves the `ingest` capability (H7): a compat plugin pushes OTLP-format
// spans through its OWN endpoint, and the kernel runs them through the SAME
// pipeline as native OTLP — normalize/redact/validate/merge/dq — with two
// kernel-enforced guarantees a plugin cannot subvert:
//   - project is the caller's (from the assertion), never the body → a plugin
//     cannot write outside its project;
//   - source is stamped "plugin:{id}" by the kernel, overwriting the body → a
//     plugin cannot forge a different source.
//
// It cannot bypass the pipeline: it is the pipeline, minus the api-key
// authenticate stage (replaced by the cap:ingest double-token gate).
type Ingest struct {
	authz   *pluginauth.Authorizer
	pipe    Ingester
	project string // the plugin's target project (lite: the default project)
}

// NewIngest builds the ingest handler. project is the tenant plugin telemetry
// lands in — resolved by the kernel (lite: the default project), NEVER from the
// request, so a plugin cannot write outside its project.
func NewIngest(authz *pluginauth.Authorizer, pipe Ingester, project string) *Ingest {
	return &Ingest{authz: authz, pipe: pipe, project: project}
}

// Register mounts POST {prefix} (e.g. /v1alpha1/plugin/ingest/traces).
func (h *Ingest) Register(mux *http.ServeMux, prefix string) {
	mux.HandleFunc(prefix+"/traces", h.traces)
}

func (h *Ingest) traces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Cold-path ingest is plugin-initiated (no user) — service-token-only (H7 #3).
	pluginID, status, err := h.authz.RequirePluginToken(r, "ingest")
	if err != nil {
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxIngestBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read error"})
		return
	}
	ing := &pipeline.Ingestion{
		Body:        body,
		ContentType: r.Header.Get("Content-Type"),
		ReceivedAt:  time.Now(),
		// Project is the plugin's own (kernel-resolved), NEVER the body. Source is
		// kernel-stamped so it cannot be forged.
		Identity: controlplane.Identity{ProjectID: h.project},
		Source:   pluginproto.PluginSubject(pluginID),
	}
	if err := h.pipe.RunPreauth(r.Context(), ing); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
