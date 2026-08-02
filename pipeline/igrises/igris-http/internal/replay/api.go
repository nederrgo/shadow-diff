package replay

import (
	"net/http"

	pkgreplay "github.com/shadow-diff/replay"
)

// Handler serves POST /v1/replay/start.
type Handler struct {
	Engine *Engine
}

// Mount registers replay admin routes on mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	var starter pkgreplay.Starter
	if h.Engine != nil {
		starter = h.Engine
	}
	(&pkgreplay.Handler{Engine: starter}).Mount(mux)
}
