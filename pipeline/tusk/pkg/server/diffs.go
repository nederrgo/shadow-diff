package server

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/shadow-diff/tusk/pkg/db"
)

func (s *HTTPServer) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		http.Error(w, "postgres not configured", http.StatusServiceUnavailable)
		return
	}
	opts := db.ListSessionsOpts{
		WithDiffsOnly: queryTruthy(r.URL.Query().Get("with_diffs")),
	}
	sessions, err := s.Store.ListSessions(r.Context(), opts)
	if err != nil {
		s.Log.Warn("ListSessions failed", "err", err)
		http.Error(w, "list sessions failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, sessions)
}

func (s *HTTPServer) handleListExecutions(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		http.Error(w, "postgres not configured", http.StatusServiceUnavailable)
		return
	}
	sessionID := r.PathValue("session_id")
	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	execs, err := s.Store.ListExecutions(r.Context(), sessionID)
	if err != nil {
		s.Log.Warn("ListExecutions failed", "err", err, "session_id", sessionID)
		http.Error(w, "list executions failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, execs)
}

func queryTruthy(v string) bool {
	switch v {
	case "1", "true", "TRUE", "yes", "on":
		return true
	default:
		return false
	}
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
	execID := r.URL.Query().Get("replay_execution_id")
	diffs, err := s.Store.GetSessionDiffs(r.Context(), sessionID, execID)
	if err != nil {
		s.Log.Warn("GetSessionDiffs failed", "err", err, "session_id", sessionID, "replay_execution_id", execID)
		http.Error(w, "get diffs failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, diffs)
}

func (s *HTTPServer) handleGetOccurrences(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		http.Error(w, "postgres not configured", http.StatusServiceUnavailable)
		return
	}
	traceID := r.URL.Query().Get("trace_id")
	signature := r.URL.Query().Get("signature")
	if traceID == "" || signature == "" {
		http.Error(w, "trace_id and signature are required", http.StatusBadRequest)
		return
	}
	execID := r.URL.Query().Get("replay_execution_id")
	out, err := s.Store.GetSignatureOccurrences(r.Context(), traceID, signature, execID)
	if err != nil {
		s.Log.Warn("GetSignatureOccurrences failed", "err", err, "trace_id", traceID, "signature", signature)
		http.Error(w, "get occurrences failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
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
	execID := r.URL.Query().Get("replay_execution_id")
	resolved, err := s.Store.ResolveExecutionID(r.Context(), sessionID, execID)
	if err != nil {
		s.Log.Warn("ResolveExecutionID failed", "err", err, "session_id", sessionID)
		http.Error(w, "resolve execution failed", http.StatusInternalServerError)
		return
	}

	// Fresh summary so a client attaching mid-session is not empty when the
	// hub cache has never seen a NOTIFY for this execution yet.
	if sum, err := s.Store.SessionSummary(r.Context(), sessionID, resolved); err == nil {
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

	updates, snapshot, unsubscribe := s.DiffHub.Subscribe(sessionID, resolved)
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
