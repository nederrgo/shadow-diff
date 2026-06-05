package dashboard

import (
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/shadow-diff/beru/internal/storage"
)

//go:embed embed/*
var embedFS embed.FS

// Handler serves dashboard pages and static assets.
type Handler struct {
	DB  *storage.DB
	Log *slog.Logger
	tpl *template.Template
}

// NewHandler loads embedded templates.
func NewHandler(db *storage.DB, log *slog.Logger) (*Handler, error) {
	if log == nil {
		log = slog.Default()
	}
	sub, err := fs.Sub(embedFS, "embed")
	if err != nil {
		return nil, err
	}
	tpl, err := template.ParseFS(sub, "layout.html", "index.html", "trace_layout.html", "trace.html")
	if err != nil {
		return nil, err
	}
	return &Handler{DB: db, Log: log, tpl: tpl}, nil
}

// Register mounts dashboard and API routes on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/dashboard/", h.handleDashboard)
	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/", http.StatusMovedPermanently)
	})
	mux.Handle("/dashboard/static/", http.StripPrefix("/dashboard/static/", h.staticHandler()))
	mux.HandleFunc("/api/v1/shadow-tests", h.handleAPIShadowTests)
	mux.HandleFunc("/api/v1/traces", h.handleAPITraces)
	mux.HandleFunc("/api/v1/traces/", h.handleAPITraceByID)
	mux.HandleFunc("/api/v1/noise/filters", h.handleAPINoiseFilters)
}

func (h *Handler) handleDashboard(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/dashboard/" {
		h.handleIndex(w, r)
		return
	}
	if strings.HasPrefix(path, "/dashboard/traces/") {
		h.handleTrace(w, r)
		return
	}
	http.NotFound(w, r)
}

func (h *Handler) staticHandler() http.Handler {
	sub, _ := fs.Sub(embedFS, "embed")
	return http.FileServer(http.FS(sub))
}
