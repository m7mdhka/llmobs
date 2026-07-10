package jobs

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Handler is the jobs ops API: on-demand trigger + run status. Gated by `actor`,
// which returns the caller's audit actor (e.g. "session:admin@x") and whether they
// are allowed (admin today; RBAC beyond admin is issue #21).
type Handler struct {
	sched *Scheduler
	actor func(*http.Request) (actor string, allowed bool)
}

func NewHandler(sched *Scheduler, actor func(*http.Request) (string, bool)) *Handler {
	return &Handler{sched: sched, actor: actor}
}

func (h *Handler) Register(mux *http.ServeMux, prefix string) {
	mux.HandleFunc(prefix+"/trigger", h.trigger)
	mux.HandleFunc(prefix+"/runs", h.runs)
}

func (h *Handler) trigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	actor, ok := h.actor(r)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct{ Plugin, Job string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.Plugin == "" || req.Job == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "plugin and job required"})
		return
	}
	runID, err := h.sched.Trigger(r.Context(), req.Plugin, req.Job, actor)
	switch {
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such job"})
	case errors.Is(err, ErrAlreadyRunning):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "job already running"})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "trigger failed"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"run_id": runID})
	}
}

func (h *Handler) runs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := h.actor(r); !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	plugin := r.URL.Query().Get("plugin")
	if plugin == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "plugin required"})
		return
	}
	runs, err := h.sched.Runs(r.Context(), plugin, 50)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "runs failed"})
		return
	}
	if runs == nil {
		runs = []Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
