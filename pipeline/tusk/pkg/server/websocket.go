package server

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shadow-diff/tusk/pkg/topology"
)

// defaultCORSOrigin is echoed when the request has no Origin (non-browser probes).
// Browsers always send Origin on cross-origin fetches; CheckOrigin gates upgrades.
const defaultCORSOrigin = "http://localhost:3000"

// writeTimeout bounds a single frame write so one wedged socket cannot pin a
// goroutine forever.
const writeTimeout = 10 * time.Second

// HTTPServer serves topology WebSockets, diff REST/WS, and health check.
type HTTPServer struct {
	Hub     *Hub
	DiffHub *DiffHub
	Store   SessionStore // nil = control-plane-only (topology still works)
	Log     *slog.Logger

	upgrader websocket.Upgrader
}

// allowedOrigin reports whether a browser Origin may open a WebSocket (or be
// reflected in CORS). Empty Origin (tests, websocat, probes) is allowed by the
// caller separately. Localhost / 127.0.0.1 on any port covers The System on
// Vite :3000 and Nginx :80.
func allowedOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1"
}

// Handler builds the mux. CORS is applied to the plain HTTP routes; the
// WebSocket handshake is gated by the upgrader's CheckOrigin instead, since
// browsers do not apply CORS to WebSocket upgrades.
func (s *HTTPServer) Handler() http.Handler {
	s.upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			// No Origin means a non-browser client (tests, websocat, probes).
			return origin == "" || allowedOrigin(origin)
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /ws/monitor", s.handleMonitor)
	mux.HandleFunc("GET /api/v1/sessions", s.handleListSessions)
	mux.HandleFunc("GET /api/v1/sessions/{session_id}/executions", s.handleListExecutions)
	mux.HandleFunc("GET /api/v1/diffs", s.handleGetDiffs)
	mux.HandleFunc("GET /api/v1/diffs/occurrences", s.handleGetOccurrences)
	mux.HandleFunc("GET /ws/diffs", s.handleDiffsWS)
	return withCORS(mux)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allow := defaultCORSOrigin
		if origin != "" && allowedOrigin(origin) {
			allow = origin
		}
		w.Header().Set("Access-Control-Allow-Origin", allow)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleMonitor streams topology graphs for the requested ShadowTest. Omitting
// both query params streams every test, which is what a list view wants.
func (s *HTTPServer) handleMonitor(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("test")
	namespace := r.URL.Query().Get("namespace")

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an error response.
		s.Log.Warn("WebSocket upgrade failed", "err", err)
		return
	}
	defer func() { _ = conn.Close() }()

	updates, snapshot, unsubscribe := s.Hub.Subscribe(namespace, name)
	defer unsubscribe()

	// Drain reads so control frames are processed and a client close is noticed.
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for _, g := range snapshot {
		if err := writeGraph(conn, g); err != nil {
			return
		}
	}

	for {
		select {
		case <-closed:
			return
		case g, ok := <-updates:
			if !ok {
				return
			}
			if err := writeGraph(conn, g); err != nil {
				s.Log.Warn("WebSocket write failed", "err", err, "test", name)
				return
			}
		}
	}
}

func writeGraph(conn *websocket.Conn, g *topology.TopologyGraph) error {
	if err := conn.SetWriteDeadline(deadlineNow()); err != nil {
		return err
	}
	return conn.WriteJSON(g)
}

func deadlineNow() time.Time {
	return time.Now().Add(writeTimeout)
}
