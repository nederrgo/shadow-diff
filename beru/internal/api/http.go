package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/shadow-diff/beru/internal/replay"
)

// Server exposes temporary HTTP endpoints for egress replay testing.
type Server struct {
	Log   *slog.Logger
	Mocks *replay.MockStore
}

type seedMockRequest struct {
	Method      string           `json:"method"`
	Host        string           `json:"host"`
	Path        string           `json:"path"`
	Body        json.RawMessage  `json:"body"`
	IgnorePaths []string         `json:"ignore_paths"`
	Response    seedMockResponse `json:"response"`
}

type seedMockResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/seed_mock", s.handleSeedMock)
	mux.HandleFunc("/v1/record_egress", s.handleRecordEgress)
	s.Log.Info("Beru HTTP API listening", "addr", addr)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleSeedMock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	s.putMockFromRequest(w, r)
}

func (s *Server) handleRecordEgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	s.putMockFromRequest(w, r)
}

func (s *Server) putMockFromRequest(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	var req seedMockRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if req.Method == "" || req.Host == "" || req.Path == "" {
		http.Error(w, "method, host, and path are required", http.StatusBadRequest)
		return
	}
	if req.Response.Status == 0 {
		http.Error(w, "response.status is required", http.StatusBadRequest)
		return
	}

	body := []byte(req.Body)
	if len(body) == 0 {
		body = []byte{}
	}

	hash, err := replay.HashRequest(req.Method, req.Host, req.Path, body, req.IgnorePaths)
	if err != nil {
		http.Error(w, "Could not hash request", http.StatusBadRequest)
		return
	}

	s.Mocks.Put(hash, replay.EarlyResponse{
		StatusCode: req.Response.Status,
		Headers:    req.Response.Headers,
		Body:       []byte(req.Response.Body),
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"hash": hash})
}
