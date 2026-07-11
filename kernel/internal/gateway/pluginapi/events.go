package pluginapi

import (
	"context"
	"net/http"

	"github.com/m7mdhka/llmobs/kernel/internal/bus"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginauth"
)

// EventBus is the subscribe surface the events endpoints need (interface-at-consumer).
type EventBus interface {
	Poll(ctx context.Context, pluginID, projectID string, topics []string, max int) ([]bus.Delivered, error)
	Ack(ctx context.Context, pluginID, projectID, topic string, upTo int64) error
}

// Events serves the `events` primitive (poll + ack), gated on cap:events. Delivery
// is at-least-once with the event id as the idempotency key; a subscriber replays
// from its offset after downtime; the tenant comes from the assertion.
type Events struct {
	authz *pluginauth.Authorizer
	bus   EventBus
}

func NewEvents(authz *pluginauth.Authorizer, b EventBus) *Events {
	return &Events{authz: authz, bus: b}
}

func (h *Events) Register(mux *http.ServeMux, prefix string) {
	mux.HandleFunc(prefix+"/poll", h.handle(h.poll))
	mux.HandleFunc(prefix+"/ack", h.handle(h.ack))
}

func (h *Events) handle(fn func(http.ResponseWriter, *http.Request, pluginauth.Caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		caller, status, err := h.authz.Require(r, "events")
		if err != nil {
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		fn(w, r, caller)
	}
}

type eventsReq struct {
	Topics []string `json:"topics,omitempty"`
	Max    int      `json:"max,omitempty"`
	Topic  string   `json:"topic,omitempty"`
	Offset int64    `json:"offset,omitempty"`
}

const maxPollTopics = 64 // per-poll topic fan-out cap (DoS bound)

func (h *Events) poll(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	var req eventsReq
	if err := decodeJSON(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	if len(req.Topics) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "topics required"})
		return
	}
	// Cap the topic fan-out: each topic costs Offset+LatestID+After per poll, so an
	// unbounded list turns one authorized call into O(topics) backend round-trips.
	if len(req.Topics) > maxPollTopics {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "too many topics"})
		return
	}
	events, err := h.bus.Poll(r.Context(), c.PluginID, c.ProjectID, req.Topics, req.Max)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "poll failed"})
		return
	}
	if events == nil {
		events = []bus.Delivered{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (h *Events) ack(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	var req eventsReq
	if err := decodeJSON(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	if req.Topic == "" || req.Offset <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "topic and offset required"})
		return
	}
	if err := h.bus.Ack(r.Context(), c.PluginID, c.ProjectID, req.Topic, req.Offset); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "ack failed"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
