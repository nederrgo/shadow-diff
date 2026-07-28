package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/shadow-diff/s3utils"
	"github.com/shadow-diff/shop/internal/replay"
)

// Server exposes HTTP endpoints for egress mock store seeding.
type Server struct {
	Log      *slog.Logger
	Mocks    *replay.MockStore
	Uploader *s3utils.BatchUploader // non-nil when OPERATING_MODE=record
	// Ready gates /healthz. False while replay-mode S3 preload is in progress.
	Ready atomic.Bool
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
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/seed_mock", s.handleSeedMock)
	mux.HandleFunc("/v1/record_egress", s.handleRecordEgress)
	s.Log.Info("Shop HTTP API listening", "addr", addr)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if !s.Ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "loading"})
		return
	}
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
	if s.Uploader != nil {
		s.recordEgressToS3(w, r)
		return
	}
	s.putMockFromRequest(w, r)
}

func (s *Server) recordEgressToS3(w http.ResponseWriter, r *http.Request) {
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
	if err := s.Uploader.Add(req); err != nil {
		s.Log.Error("egress S3 buffer failed", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	// Same hash contract as putMockFromRequest so Kaisel can log the Envoy
	// lookup key (and bats can assert it). Body is buffered to S3 only.
	key := replay.TraceKey(req.TraceID, req.Method, replay.HostWithoutPort(req.Host), req.Path)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"hash": key})
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

	key := replay.TraceKey(req.TraceID, req.Method, replay.HostWithoutPort(req.Host), req.Path)
	s.Mocks.Put(key, replay.EarlyResponse{
		StatusCode: req.Response.Status,
		Headers:    req.Response.Headers,
		Body:       []byte(req.Response.Body),
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"hash": key})
}
