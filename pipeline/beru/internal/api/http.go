package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/shadow-diff/beru/internal/dashboard"
	"github.com/shadow-diff/beru/internal/otlp"
	"github.com/shadow-diff/beru/internal/replay"
	"github.com/shadow-diff/beru/internal/roles"
	"github.com/shadow-diff/beru/internal/storage"
	v2engine "github.com/shadow-diff/beru/internal/v2/engine"
	v2report "github.com/shadow-diff/beru/internal/v2/report"
)

// Server exposes HTTP endpoints for egress replay testing and the dashboard.
type Server struct {
	Log        *slog.Logger
	Mocks     *replay.MockStore
	Router    *v2engine.TraceRouter
	OTLP       *otlp.Server
	DB         *storage.DB
	Dashboard  *dashboard.Handler
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

type egressDiffRequest struct {
	TraceID        string          `json:"trace_id"`
	Workload       string          `json:"workload"`
	Protocol       string          `json:"protocol"`
	Payload        json.RawMessage `json:"payload"`
	ShadowTestName string          `json:"shadow_test_name"`
}

func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/v1/seed_mock", s.handleSeedMock)
	mux.HandleFunc("/v1/record_egress", s.handleRecordEgress)
	mux.HandleFunc("/api/v1/egress/diff", s.handleEgressDiff)
	mux.HandleFunc("/api/v1/ingest/wire", s.handleWireIngest)
	if s.OTLP != nil {
		mux.HandleFunc("/v1/traces", s.OTLP.HandleHTTP)
	}
	if s.Dashboard != nil {
		s.Dashboard.Register(mux)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/dashboard/", http.StatusTemporaryRedirect)
	})
	s.Log.Info("Beru HTTP API listening", "addr", addr)
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

func (s *Server) handleEgressDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Router == nil {
		http.Error(w, "Egress diff not configured", http.StatusServiceUnavailable)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	var req egressDiffRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if req.TraceID == "" || req.Workload == "" || req.Protocol == "" {
		http.Error(w, "trace_id, workload, and protocol are required", http.StatusBadRequest)
		return
	}
	if !roles.IsValid(req.Workload) {
		http.Error(w, "workload is invalid", http.StatusBadRequest)
		return
	}
	if len(req.Payload) == 0 || !json.Valid(req.Payload) {
		http.Error(w, "payload must be valid JSON", http.StatusBadRequest)
		return
	}

	shadowTest := req.ShadowTestName
	if shadowTest == "" && s.DB != nil {
		shadowTest = s.DB.DefaultShadowTestName()
	}
	rawReport, err := v2report.FromEgress(req.TraceID, req.Workload, req.Protocol, shadowTest, req.Payload)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	s.Router.Route(rawReport)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]struct{}{})
}

func (s *Server) handleWireIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Router == nil {
		http.Error(w, "Wire ingest not configured", http.StatusServiceUnavailable)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	var env v2report.NetworkEventEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if env.ShadowTestName == "" && s.DB != nil {
		env.ShadowTestName = s.DB.DefaultShadowTestName()
	}
	rawReport, err := v2report.FromWireEnvelope(&env)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if rawReport.ShadowTestName == "" && s.DB != nil {
		rawReport.ShadowTestName = s.DB.DefaultShadowTestName()
	}
	s.Router.Route(rawReport)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]struct{}{})
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
