package replay

import (
	"encoding/json"
	"net/http"
)

// Starter is implemented by Engine[T].
type Starter interface {
	Start() (total int, already bool)
}

// Handler serves POST /v1/replay/start (Monarch contract: 202 / 409 / 503).
type Handler struct {
	Engine Starter
}

// Mount registers replay admin routes on mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/v1/replay/start", h.handleStart)
}

func (h *Handler) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.Engine == nil {
		http.Error(w, "replay engine not configured", http.StatusServiceUnavailable)
		return
	}
	n, already := h.Engine.Start()
	if already {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":        "replay_in_progress",
			"total_records": n,
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":        "replay_started",
		"total_records": n,
	})
}
