package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/shadow-diff/shop/internal/replay"
)

// Server exposes HTTP endpoints for egress mock store seeding.
type Server struct {
	Log   *slog.Logger
	Mocks *replay.MockStore
}

type seedMockRequest struct {
	TraceID  string           `json:"trace_id"`
	Method   string           `json:"method"`
	Host     string           `json:"host"`
	Path     string           `json:"path"`
	Response seedMockResponse `json:"response"`
}

type seedMockResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/v1/seed_mock", s.handleSeedMock)
	mux.HandleFunc("/v1/record_egress", s.handleRecordEgress)
	s.Log.Info("Shop HTTP API listening", "addr", addr)
	return http.ListenAndServe(addr, mux)
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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
	if req.TraceID == "" {
		http.Error(w, "trace_id is required", http.StatusBadRequest)
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

	key := replay.TraceKey(req.TraceID, req.Method, req.Host, req.Path)
	s.Mocks.Put(key, replay.EarlyResponse{
		StatusCode: req.Response.Status,
		Headers:    req.Response.Headers,
		Body:       []byte(req.Response.Body),
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"hash": key})
}
