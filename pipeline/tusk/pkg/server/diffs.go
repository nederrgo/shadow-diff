package server

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/websocket"
)

func (s *HTTPServer) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		http.Error(w, "postgres not configured", http.StatusServiceUnavailable)
		return
	}
	sessions, err := s.Store.ListSessions(r.Context())
	if err != nil {
		s.Log.Warn("ListSessions failed", "err", err)
		http.Error(w, "list sessions failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, sessions)
}

func (s *HTTPServer) handleGetDiffs(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		http.Error(w, "postgres not configured", http.StatusServiceUnavailable)
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	diffs, err := s.Store.GetSessionDiffs(r.Context(), sessionID)
	if err != nil {
		s.Log.Warn("GetSessionDiffs failed", "err", err, "session_id", sessionID)
		http.Error(w, "get diffs failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, diffs)
}

func (s *HTTPServer) handleDiffsWS(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil || s.DiffHub == nil {
		http.Error(w, "postgres not configured", http.StatusServiceUnavailable)
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}

	// Fresh summary so a client attaching mid-session is not empty when the
	// hub cache has never seen a NOTIFY for this session yet.
	if sum, err := s.Store.SessionSummary(r.Context(), sessionID); err == nil {
		s.DiffHub.CacheSummary(sum)
	} else {
		s.Log.Warn("SessionSummary failed on WS connect", "err", err, "session_id", sessionID)
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.Log.Warn("Diffs WebSocket upgrade failed", "err", err)
		return
	}
	defer func() { _ = conn.Close() }()

	updates, snapshot, unsubscribe := s.DiffHub.Subscribe(sessionID)
	defer unsubscribe()

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for _, f := range snapshot {
		if err := writeDiffFrame(conn, f); err != nil {
			return
		}
	}

	for {
		select {
		case <-closed:
			return
		case f, ok := <-updates:
			if !ok {
				return
			}
			if err := writeDiffFrame(conn, f); err != nil {
				s.Log.Warn("Diffs WebSocket write failed", "err", err, "session_id", sessionID)
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// Headers may already be written.
		return
	}
}

func writeDiffFrame(conn *websocket.Conn, f *DiffFrame) error {
	if err := conn.SetWriteDeadline(deadlineNow()); err != nil {
		return err
	}
	return conn.WriteJSON(f)
}
